package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/template"
)

var HELM_BIN = "/bin/helm"
var KUBECONFIG = "/root/.kube/kubeconfig"

type (
	// Config maps the params we need to run Helm
	Config struct {
		APIServer      string   `json:"api_server"`
		Token          string   `json:"token"`
		ServiceAccount string   `json:"service_account"`
		KubeConfig     string   `json:"kube_config"`
		HelmCommand    []string `json:"helm_command"`
		SkipTLSVerify  bool     `json:"tls_skip_verify"`
		Namespace      string   `json:"namespace"`
		Release        string   `json:"release"`
		Chart          string   `json:"chart"`
		Version        string   `json:"version"`
		Values         string   `json:"values"`
		ValuesFiles    string   `json:"values_files"`
		Debug          bool     `json:"debug"`
		DryRun         bool     `json:"dry_run"`
		Secrets        []string `json:"secrets"`
		Prefix         string   `json:"prefix"`
		TillerNs       string   `json:"tiller_ns"`
		Wait           bool     `json:"wait"`
		RecreatePods   bool     `json:"recreate_pods"`
		Upgrade        bool     `json:"upgrade"`
		CanaryImage    bool     `json:"canary_image"`
		ClientOnly     bool     `json:"client_only"`
		ReuseValues    bool     `json:"reuse_values"`
		Timeout        string   `json:"timeout"`
		Force          bool     `json:"force"`
		HelmRepos      []string `json:"helm_repos"`
	}
	// Plugin default
	Plugin struct {
		Config Config
	}
)

func setHelmHelp(p *Plugin) {
	p.Config.HelmCommand = []string{""}
}
func setDeleteEventCommand(p *Plugin) {
	upgrade := make([]string, 2)
	upgrade[0] = "delete"
	upgrade[1] = p.Config.Release

	p.Config.HelmCommand = upgrade
}

func setPushEventCommand(p *Plugin) {
	upgrade := make([]string, 2)
	upgrade[0] = "upgrade"
	upgrade[1] = "--install"

	if p.Config.Release != "" {
		upgrade = append(upgrade, p.Config.Release)
	}
	upgrade = append(upgrade, p.Config.Chart)
	if p.Config.Version != "" {
		upgrade = append(upgrade, "--version")
		upgrade = append(upgrade, p.Config.Version)
	}
	if p.Config.Values != "" {
		upgrade = append(upgrade, "--set")
		upgrade = append(upgrade, unQuote(p.Config.Values))
	}
	if p.Config.ValuesFiles != "" {
		for _, valuesFile := range strings.Split(p.Config.ValuesFiles, ",") {
			upgrade = append(upgrade, "--values")
			upgrade = append(upgrade, valuesFile)
		}
	}
	if p.Config.Namespace != "" {
		upgrade = append(upgrade, "--namespace")
		upgrade = append(upgrade, p.Config.Namespace)
	}
	if p.Config.TillerNs != "" {
		upgrade = append(upgrade, "--tiller-namespace")
		upgrade = append(upgrade, p.Config.TillerNs)
	}
	if p.Config.DryRun {
		upgrade = append(upgrade, "--dry-run")
	}
	if p.Config.Debug {
		upgrade = append(upgrade, "--debug")
	}
	if p.Config.Wait {
		upgrade = append(upgrade, "--wait")
	}
	if p.Config.RecreatePods {
		upgrade = append(upgrade, "--recreate-pods")
	}
	if p.Config.ReuseValues {
		upgrade = append(upgrade, "--reuse-values")
	}
	if p.Config.Timeout != "" {
		upgrade = append(upgrade, "--timeout")
		upgrade = append(upgrade, p.Config.Timeout)
	}
	if p.Config.Force {
		upgrade = append(upgrade, "--force")
	}
	p.Config.HelmCommand = upgrade

}

func setHelmCommand(p *Plugin) {
	buildEvent := os.Getenv("DRONE_BUILD_EVENT")
	switch buildEvent {
	case "push":
		setPushEventCommand(p)
	case "tag":
		setPushEventCommand(p)
	case "deployment":
		setPushEventCommand(p)
	case "delete":
		setDeleteEventCommand(p)
	default:
		setHelmHelp(p)
	}

}

var repoExp = regexp.MustCompile(`^(?P<name>[\w-]+)=(?P<url>(http|https)://[\w-./:]+)`)

// parseRepo returns map of regex capture groups (name, url)
func parseRepo(repo string) (map[string]string, error) {
	matches := repoExp.FindStringSubmatch(repo)
	if len(matches) < 1 {
		return nil, fmt.Errorf("Invalid repo definition: %s", repo)
	}
	result := make(map[string]string)
	for i, name := range repoExp.SubexpNames() {
		if i != 0 {
			result[name] = matches[i]
		}
	}
	return result, nil
}

func doHelmRepoAdd(repo string) ([]string, error) {
	repoMap, err := parseRepo(unQuote(repo))
	if err != nil {
		return nil, err
	}
	repoAdd := []string{
		"repo",
		"add",
		repoMap["name"],
		repoMap["url"],
	}
	return repoAdd, nil
}

func doHelmInit(p *Plugin) []string {
	init := make([]string, 1)
	init[0] = "init"
	if p.Config.TillerNs != "" {
		init = append(init, "--tiller-namespace")
		init = append(init, p.Config.TillerNs)
	}
	if p.Config.ClientOnly {
		init = append(init, "--client-only")
	}
	if p.Config.Upgrade {
		init = append(init, "--upgrade")
	}
	if p.Config.CanaryImage {
		init = append(init, "--canary-image")
	}

	return init

}

// Exec default method
func (p *Plugin) Exec() error {
	if p.Config.Debug {
		p.debugEnv()
	}

	// create /root/.kube/config file if not exists
	if _, err := os.Stat(p.Config.KubeConfig); os.IsNotExist(err) {
		resolveSecrets(p)
		if p.Config.APIServer == "" {
			return fmt.Errorf("Error: API Server is needed to deploy.")
		}
		if p.Config.Token == "" {
			return fmt.Errorf("Error: Token is needed to deploy.")
		}

		initialiseKubeconfig(&p.Config, KUBECONFIG, p.Config.KubeConfig)
	}

	if p.Config.Debug {
		p.debug()
	}

	init := doHelmInit(p)
	err := runCommand(init)
	if err != nil {
		return fmt.Errorf("Error running helm command: " + strings.Join(init[:], " "))
	}

	if len(p.Config.HelmRepos) > 0 {
		for _, repo := range p.Config.HelmRepos {
			repoAdd, err := doHelmRepoAdd(repo)
			if err == nil {
				if p.Config.Debug {
					log.Println("adding helm repo: " + strings.Join(repoAdd[:], " "))
				}

				if err = runCommand(repoAdd); err != nil {
					return fmt.Errorf("Error adding helm repo: " + err.Error())
				}
			} else {
				return err
			}
		}
	}

	setHelmCommand(p)

	if p.Config.Debug {
		log.Println("helm command: " + strings.Join(p.Config.HelmCommand[:], " "))
	}

	err = runCommand(p.Config.HelmCommand)
	if err != nil {
		return fmt.Errorf("Error running helm command: " + strings.Join(p.Config.HelmCommand[:], " "))
	}

	return nil
}

func initialiseKubeconfig(params *Config, source string, target string) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	// parse template
	t, _ := template.ParseFiles(source)
	// execute template
	return t.Execute(f, params)
}

func runCommand(params []string) error {
	cmd := new(exec.Cmd)
	cmd = exec.Command(HELM_BIN, params...)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	return err
}

func resolveSecrets(p *Plugin) {
	p.Config.Values = resolveEnvVar(p.Config.Values, p.Config.Prefix, p.Config.Debug)
	p.Config.APIServer = resolveEnvVar("${API_SERVER}", p.Config.Prefix, p.Config.Debug)
	p.Config.Token = resolveEnvVar("${KUBERNETES_TOKEN}", p.Config.Prefix, p.Config.Debug)
	p.Config.ServiceAccount = resolveEnvVar("${SERVICE_ACCOUNT}", p.Config.Prefix, p.Config.Debug)
	if p.Config.ServiceAccount == "" {
		p.Config.ServiceAccount = "helm"
	}
}

// getEnvVars will return [${TAG} {TAG} TAG]
func getEnvVars(envvars string) [][]string {
	re := regexp.MustCompile(`\$(\{?(\w+)\}?)\.?`)
	extracted := re.FindAllStringSubmatch(envvars, -1)
	return extracted
}

func resolveEnvVar(key string, prefix string, debug bool) string {
	envvars := getEnvVars(key)
	return replaceEnvvars(envvars, prefix, key, debug)
}

func replaceEnvvars(envvars [][]string, prefix string, s string, debug bool) string {
	for _, envvar := range envvars {
		envvarName := envvar[0]
		envvarKey := envvar[2]
		prefixedKey := strings.ToUpper(prefix + "_" + envvarKey)
		envval := os.Getenv(prefixedKey)
		if debug {
			fmt.Printf("-ReplVar: %s => %s-- %s\n", prefixedKey, envvarKey, envval)
		}

		if envval == "" {
			envval = os.Getenv(envvarKey)
		}

		if strings.Contains(s, envvarName) {
			s = strings.Replace(s, envvarName, envval, -1)
		}
	}

	return s
}

// unQuote removes quotes if present
func unQuote(s string) string {
	unquoted, err := strconv.Unquote(s)
	if err != nil {
		// ignore error and return original string
		return s
	}
	return unquoted
}

func (p *Plugin) debugEnv() {
	// debug env vars
	for _, e := range os.Environ() {
		fmt.Println("-Var:--", e)
	}
}

func (p *Plugin) debug() {
	fmt.Println(p)
	// debug plugin obj
	fmt.Printf("Api server: %s \n", p.Config.APIServer)
	fmt.Printf("Values: %s \n", p.Config.Values)
	fmt.Printf("Secrets: %s \n", p.Config.Secrets)
	fmt.Printf("Helm Repos: %s \n", p.Config.HelmRepos)
	fmt.Printf("ValuesFiles: %s \n", p.Config.ValuesFiles)
	kubeconfig, err := ioutil.ReadFile(KUBECONFIG)
	if err == nil {
		fmt.Println(string(kubeconfig))
	}

	config, err := ioutil.ReadFile(p.Config.KubeConfig)
	if err == nil {
		fmt.Println(string(config))
	}
}
