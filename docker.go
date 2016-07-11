package main

import (
	"io"
	"io/ioutil"
	"os/exec"
	"os"
	"archive/tar"
	"path/filepath"
	"strings"
	"fmt"

	"github.com/samalba/dockerclient"
)

type Docker struct {
	OutStream io.Writer
	InStream  io.Reader
	Discard   bool
	Endpoint	string
}

// Callback used to listen to Docker's events
func eventCallback(event *dockerclient.Event, ec chan error, args ...interface{}) {
    Debugf("Received event: %#v\n", *event)
}

// docker runs docker command
func (d *Docker) execute(args ...string) error {
	cmd := exec.Command("docker", args...)

	cmd.Stdin = d.InStream
	cmd.Stderr = d.OutStream
	cmd.Stdout = d.OutStream

	if d.Discard {
		cmd.Stderr = ioutil.Discard
		cmd.Stdout = ioutil.Discard
	}

	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

// Does the Docker build using the dockerclient go lib (Not yet used..)
func (d *Docker) build(path string, image string) error {
	docker, _ := dockerclient.NewDockerClient(d.Endpoint, nil)


	file, err := os.Create("/tmp/app.tar")
	defer file.Close()
	d.tarit(".", "/tmp/app.tar")

	//we have a tarball, let's build
	Debugf("Preparation for build completed, starting Docker build")
	dockerBuildContext, err := os.Open("/tmp/app.tar")
	d.checkError(err)
	defer dockerBuildContext.Close()
	buildImageConfig := &dockerclient.BuildImage{
		Context:				dockerBuildContext,
		RepoName:				image,
		SuppressOutput:	false,
	}
	reader, err := docker.BuildImage(buildImageConfig)
	defer reader.Close()

	d.checkError(err)


	//Enable this if you want verbose debug output from the Docker daemon
	docker.StartMonitorEvents(eventCallback, nil)

	if err != nil {
		return err
	} else {
		return nil
	}

}

// create and run a Docker container using the dockerclient go lib
func (d *Docker) createAndRun(image string, env []string, port string) error {
	docker, _ := dockerclient.NewDockerClient(d.Endpoint, nil)

	//remove any previous containers with our name
	docker.RemoveContainer(image, true, true)

	containerConfig := &dockerclient.ContainerConfig{
			Image: 					image,
			Tty: 						true,
			Env:						env,
			ExposedPorts:		map[string]struct{}{
				port: {},
			},
	}
	Debugf("Creating container: %s", image)
	containerId, err := docker.CreateContainer(containerConfig, image, nil)
	d.checkError(err)

	done := make(chan struct{})
	if body, err := docker.AttachContainer(image, &dockerclient.AttachOptions{
		Stdout: true,
		Stream: true,
	}); err != nil {
		panic(err)
	} else {
		go func() {
			defer body.Close()

			if _, err := io.Copy(os.Stdout, body); err != nil {
				panic(err)
			}
			close(done)
		}()
	}


	portBinding := map[string][]dockerclient.PortBinding{}
	portBinding[port] = []dockerclient.PortBinding{
		{
			HostPort: port,
		},

	}
	hostConfig := &dockerclient.HostConfig{
		PortBindings: portBinding,

	}
	Debugf("Starting container: %s (%s)", containerId, image)
	if err := docker.StartContainer(containerId, hostConfig); err != nil {
		Debugf("ERROR: %s", err)
	}
	<- done

	//Enable this if you want verbose debug output from the Docker daemon
	// docker.StartMonitorEvents(eventCallback, nil)
	return nil
}


//convenience method for tarring up a directory for use in docker.BuildImage calls (tar is Docker context)
func (d *Docker) tarit(source, target string) error {
	filename := filepath.Base(source)
	Debugf("Filename: %s", filename)
	//target = filepath.Join(target, fmt.Sprintf("%s.tar", filename))
	Debugf("Target: %s", target)
	tarfile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer tarfile.Close()

	tarball := tar.NewWriter(tarfile)
	defer tarball.Close()

	info, err := os.Stat(source)
	if err != nil {
		return nil
	}

	var baseDir string
	if info.IsDir() {
		baseDir = filepath.Base(source)
	}

	return filepath.Walk(source,
	func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		if baseDir != "" {
			header.Name = filepath.Join(baseDir, strings.TrimPrefix(path, source))
		}

		if err := tarball.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tarball, file)
		return err
	})
}

//simple error check and bail function
func (d *Docker) checkError(err error)  {
	if err != nil {
		fmt.Fprintf(d.OutStream,"(cf-local-push) [Error]: %s",err)
		os.Exit(99)
	}

}
