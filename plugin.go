package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"github.com/cloudfoundry/cli/plugin"
	"github.com/tcnksm/go-input"
	"gopkg.in/yaml.v2"
	"github.com/fatih/color"
)

// Exit codes are int values that represent an exit code
// for a particular error.
const (
	ExitCodeOK    int = 0
	ExitCodeError int = 1 + iota
)

// EnvDebug is environmental variable for enabling debug
// output
const EnvDebug = "DEBUG_PLUGIN"

const (
	// DefaultPort is default port number
	DefaultPort = "8080"

	// DefaultImageName
	DefaultImageName = "cf-local-push"

	// Dockerfile is file name of Dockerfile
	Dockerfile = "Dockerfile"

	// DockerUser to exec command to container
	DockerUser = "vcap"
)

type Manifest struct {
	Applications []Application			`yaml: "applications"`
}

type Application struct {
	Name 						string 									`yaml: "name"`
	Memory	 				string 									`yaml: "memory"`
	Instances 			string 									`yaml: "instances"`
	Domain					string 									`yaml: "domain"`
	Host						string									`yaml: "host"`
	Env			 				map[string]string				`yaml: "env"`
}

func (m *Manifest) Parse (data []byte, p LocalPush) int {
	if err := yaml.Unmarshal(data, m); err != nil {
		fmt.Fprintf(p.OutStream, "Errpr: [%s]", err)
		return ExitCodeError
	}
	return ExitCodeOK
}

// dockerfileText is used for build docker image for target application.
var dockerfileText = `FROM tcnksm/cf-buildstep:latest
ENV USER vcap
ADD . /app
RUN /build/builder
CMD /start web`

// Debugf prints debug output when EnvDebug is given
func Debugf(format string, args ...interface{}) {
	if env := os.Getenv(EnvDebug); len(env) != 0 {
		fmt.Fprintf(os.Stdout, color.CyanString("[DEBUG] "+format+"\n"), args...)
	}
}

// LocalPush
type LocalPush struct {
	OutStream io.Writer
	InStream  io.Reader
}

// Run starts plugin process.
func (p *LocalPush) Run(cliConn plugin.CliConnection, arg []string) {
	Debugf("Run local-push plugin")
	Debugf("Arg: %#v", arg)


	// Ensure local-push is called.
	// Plugin is also called when install/uninstall via cf command.
	// Ignore such other calls.
	if len(arg) < 1 || arg[0] != Name {
		os.Exit(ExitCodeOK)
	}

	// Read CLI context (Currently, ctx val is not used but in future it should).
	ctx, err := NewCLIContext(cliConn)
	if err != nil {
		fmt.Fprintf(p.OutStream, "Failed to read cf command context: %s\n", err)
		os.Exit(ExitCodeError)
	}

	// Call run instead of doing the work here so we can use
	// `defer` statements within the function and have them work properly.
	// (defers aren't called with os.Exit)
	os.Exit(p.run(ctx, arg[1:]))
}

// run runs local-push it returns exit code.
func (p *LocalPush) run(ctx *CLIContext, args []string) int {

	var (
		port    string
		image   string
		enter   bool
		version bool
	)

	flags := flag.NewFlagSet("plugin", flag.ContinueOnError)
	flags.SetOutput(p.OutStream)
	flags.Usage = func() {
		fmt.Fprintln(p.OutStream, p.Usage())
	}

	flags.StringVar(&port, "port", DefaultPort, "")
	flags.StringVar(&port, "p", DefaultPort, "(Short)")

	flags.StringVar(&image, "image", DefaultImageName, "")
	flags.StringVar(&image, "i", DefaultImageName, "(Short)")

	flags.BoolVar(&enter, "enter", false, "")
	flags.BoolVar(&version, "version", false, "")
	flags.BoolVar(&version, "v", false, "(Short)")

	if err := flags.Parse(args); err != nil {
		return ExitCodeError
	}

	if version {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "%s v%s", Name, VersionStr())

		if len(GitCommit) != 0 {
			fmt.Fprintf(&buf, " (%s)", GitCommit)
		}

		fmt.Fprintln(p.OutStream, buf.String())
		return ExitCodeOK
	}

	ui := &input.UI{
		Writer: p.OutStream,
		Reader: p.InStream,
	}

	// Use same name as image
	container := image

	docker := &Docker{
		OutStream: p.OutStream,
		InStream:  p.InStream,
		Discard:   false,
		Endpoint:	 os.Getenv("DOCKER_HOST"),
	}

	//Read the manifest.yml, if present
	data, err := ioutil.ReadFile("manifest.yml")
	if err != nil {
		fmt.Fprintln(p.OutStream, color.RedString("No manfest.yml was found!"))
	}
	manifestFile, err := os.Open("manifest.yml")
	appPath,err := filepath.Abs(filepath.Dir(manifestFile.Name()))
	Debugf("App content path: %s", appPath)


	manifest := Manifest{}
	manifest.Parse(data, *p)

	// Check docker is installed or not.
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintf(p.OutStream, color.RedString("docker command is not found in your $PATH. Install it before.\n"))
		return ExitCodeError
	}

	// Enter the container
	if enter {
		fmt.Fprintf(p.OutStream, color.GreenString("(cf-local-push) Enter container\n"))
		err := docker.execute("exec",
			"--interactive",
			"--tty",
			"--user", DockerUser,
			container,
			"/bin/bash",
		)

		if err != nil {
			fmt.Fprintf(p.OutStream, color.RedString("Failed to enter the container %s: %s", container, err))
			return ExitCodeError
		}

		return ExitCodeOK
	}

	// Check Dockerfile is exist or not.
	// If it's exist, ask user to overwriting.
	if _, err := os.Stat(Dockerfile); !os.IsNotExist(err) {
		fmt.Fprintf(p.OutStream, color.YellowString("Dockerfile is already exist\n"))
		query := color.YellowString("Overwrite Dockerfile? [yN]")
		ans, err := ui.Ask(query, &input.Options{
			Default:     "N",
			HideDefault: true,
			HideOrder:   true,
			Required:    true,
			Loop:        true,
			ValidateFunc: func(s string) error {
				if s != "y" && s != "N" {
					return fmt.Errorf(color.RedString("input must be 'y' or 'N'"))
				}
				return nil
			},
		})

		if err != nil {
			fmt.Fprintf(p.OutStream, color.RedString("Failed to read input: %s\n", err))
			return ExitCodeError
		}

		// Stop execution
		if ans != "y" {
			fmt.Fprintf(p.OutStream, color.RedString("Aborting\n"))
			return ExitCodeOK
		}
	}

	fmt.Fprintf(p.OutStream, color.GreenString("(cf-local-push) Generate Dockerfile\n"))
	f, err := os.Create("Dockerfile")
	if err != nil {
		fmt.Fprintf(p.OutStream, "%s\n", err)
		return ExitCodeError
	}

	if _, err := f.Write([]byte(dockerfileText)); err != nil {
		fmt.Fprintf(p.OutStream, "%s\n", err)
		return ExitCodeError
	}

	fmt.Fprintf(p.OutStream, color.GreenString("(cf-local-push) Start building docker image\n"))

	if err := docker.execute("build", "-t", image, "."); err != nil {
		fmt.Fprintf(p.OutStream, "%s\n", err)
		return ExitCodeError
	}

	//gather up our environment variables from the parsed manifest.yml file
	envs := []string{}
	for key, val := range manifest.Applications[0].Env {
		envs = append(envs, fmt.Sprintf("%s=%s", key, val))
	}

	Debugf("Discovered env vars from manifest.yml: %s", envs)

	fmt.Fprintf(p.OutStream, color.GreenString("(cf-local-push) Starting and attaching to docker container\n"))

	// errCh
	errCh := make(chan error, 1)

	// port mapping settings
	//portMap := fmt.Sprintf("%s:%s", port, port)
	portEnv := fmt.Sprintf("PORT=%s", port)
	portEnvVcap := fmt.Sprintf("VCAP_APP_PORT=%s", port)

	envs = append(envs, portEnv)
	envs = append(envs, portEnvVcap)

	go func() {
		// Debugf("Run command: docker run -p %s -e %s -e %s--name %s %s",
		// 	portMap, portEnv, portEnvVcap, container, image)
		errCh <- docker.createAndRun(image, envs, port)
	}()

	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, os.Interrupt)

	select {
	case <-sigCh:
		fmt.Fprintln(p.OutStream, color.GreenString("Interrupt: Stop and remove container (It takes a few seconds...)"))

		// Don't output
		docker.Discard = true

		// Stop & Remove docker container
		docker.execute("stop", container)
		docker.execute("rm", container)

		return ExitCodeOK
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(p.OutStream, color.RedString("Failed to run container %s: %s\n", container, err))
			return ExitCodeError
		}
		return ExitCodeOK
	}
}

func (p *LocalPush) GetMetadata() plugin.PluginMetadata {
	return plugin.PluginMetadata{
		Name:    Name,
		Version: Version,
		Commands: []plugin.Command{
			{
				Name:     "local-push",
				HelpText: "Push cf app on local Docker container",
				UsageDetails: plugin.Usage{
					Usage: p.Usage(),
				},
			},
		},
	}
}

func (p *LocalPush) Usage() string {
	text := `cf local-push [options]

local-push command tells cf to deploy current working directory app on
local docker container. You need to prepare docker environment before.

local-push remove the container after stop the container.

Options:

  -port PORT      Port number to map to docker container. You can access
                  to application via this port. By default, '8080' is used.
                  If you use docker machine for running docker, you can
                  access application by 'curl $(docker-machine ip):PORT)'.

  -image NAME     Docker image name. By default, 'local-push' is used.

  -enter          Enter the container which starts by 'local-push'.
                  You must use this option after exec 'local-push' and
                  while running. You can regard this as 'ssh'.

  -version        Show version and quit.
`
	return text
}
