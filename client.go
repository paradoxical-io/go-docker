package docker

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type ContainerPort int

const ContainerToLocalhostDNS = "docker.for.mac.localhost"

func RequireDocker(t *testing.T) {
	if !DockerExists() {
		t.Skipf("Docker tests ignored because either docker isn't installed or the docker daemon isn't running")
	}
}

func DockerExists() bool {
	c, err := client.NewEnvClient()
	if err != nil {
		return false
	}

	if _, err := c.Ping(context.Background()); err != nil {
		return false
	}

	return true
}

type Image struct {
	client    *client.Client
	ImageName string
}

func (i Image) Delete() {
	_, _ = i.client.ImageRemove(context.Background(), i.ImageName, types.ImageRemoveOptions{
		Force:         true,
		PruneChildren: true,
	})
}

// StartContainer starts a docker container and names the container with the supplied prefix
func StartContainer(req NewContainerRequest, prefix string) (*DockerContainer, error) {
	client, err := client.NewEnvClient()
	if err != nil {
		return nil, err
	}

	var binds []mount.Mount
	for source, dest := range req.VolumeMounts {
		fullPath, _ := filepath.Abs(source)
		binds = append(binds, mount.Mount{
			Type:   mount.TypeBind,
			Source: fullPath,
			Target: dest,
		})
	}

	c := container.Config{
		Image: req.Image,
		Cmd:   req.Args,
	}

	// set our env vars
	for k, v := range req.EnvVars {
		c.Env = append(c.Env, fmt.Sprintf("%s=%s", k, v))
	}

	h := container.HostConfig{
		Mounts: binds,
	}

	portMaps := make(map[int]int)
	exposedPorts := make(nat.PortSet)
	// create random port maps
	if req.Ports != nil {
		mapping := nat.PortMap{}
		for _, port := range req.Ports {
			freePort, err := getFreePort()
			if err != nil {
				return nil, err
			}

			dockerPort := nat.Port(strconv.Itoa(port) + "/tcp")
			mapping[dockerPort] = []nat.PortBinding{{
				HostIP:   "",
				HostPort: strconv.Itoa(freePort),
			}}
			portMaps[port] = freePort
			exposedPorts[dockerPort] = struct{}{}
		}

		h.PortBindings = mapping
	}

	c.ExposedPorts = exposedPorts

	// check if the image exists
	_, _, err = client.ImageInspectWithRaw(context.Background(), req.Image)
	// if we're not going to use a cached container always make sure to pull it
	if err != nil || req.PullAlways {
		if err := pullImage(client, req.Image); err != nil {
			return nil, err
		}
	}

	res, err := client.ContainerCreate(context.Background(), &c, &h, nil, prefix+"-"+uuid.New().String())
	if err != nil {
		return nil, err
	}

	if err := client.ContainerStart(context.Background(), res.ID, types.ContainerStartOptions{}); err != nil {
		return nil, err
	}

	logrus.Infof("Started container %s with id %s", req.Image, res.ID)

	return &DockerContainer{
		portMappings: portMaps,
		id:           res.ID,
		client:       client,
	}, nil
}

func pullImage(client *client.Client, image string) error {
	logrus.Infof("Pulling container %s", image)

	res, err := client.ImagePull(context.Background(), image, types.ImagePullOptions{})
	if err != nil {
		return err
	}

	b, err := ioutil.ReadAll(res)
	if err != nil {
		return err
	}

	logrus.Info(string(b))

	return nil
}

// finds an open port and verifies its free
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = l.Close()
	}()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type NewContainerRequest struct {
	Image        string
	Ports        []int
	Args         []string
	PullAlways   bool
	VolumeMounts map[string]string
	EnvVars      map[string]string
}

type DockerContainer struct {
	portMappings map[int]int
	id           string
	client       *client.Client
}

// WaitForPortToOpen queries the container port and checks to see when it's open
func (d DockerContainer) WaitForPortToOpen(port ContainerPort, timeout time.Duration) error {
	return waitFor(func() error {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("", strconv.Itoa(int(port))), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		return err
	}, timeout)
}

// WaitForLogLine queries the container logs and verifies that log subtext exists in a log line
// this is important to make sure to find out if the container's application is ready for
// use.  You can't assume a container is ready for action _just_ by the container starting up
// Logs are expected to be plaintext newline separated '\n'
func (d DockerContainer) WaitForLogLine(text string, timeout time.Duration) error {
	return waitFor(func() error {
		reader, err := d.client.ContainerLogs(context.Background(), d.id, types.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
		})
		if err != nil {
			return err
		}

		b, err := ioutil.ReadAll(reader)
		if err != nil {
			return err
		}

		for _, logLine := range strings.Split(string(b), "\n") {
			if strings.Contains(logLine, text) {
				return nil
			}
		}

		return errors.Errorf("No log found")
	}, timeout)
}

func waitFor(predicate func() error, timeout time.Duration) error {
	end := time.Now().Add(timeout)

	ticker := time.NewTicker(50 * time.Millisecond)

	for range ticker.C {
		if err := predicate(); err == nil {
			return nil
		}

		if time.Now().After(end) {
			ticker.Stop()
		}
	}

	return errors.New("Predicate never succeeded")
}

func (d DockerContainer) PortMapping(port int) ContainerPort {
	return ContainerPort(d.portMappings[port])
}

func (d DockerContainer) Close() {
	d.CloseWithTimeout(1 * time.Second)
}

func (d DockerContainer) CloseWithTimeout(timeout time.Duration) {
	// try and graceful stop, if it doesn't, just kill it
	if err := d.client.ContainerStop(context.Background(), d.id, &timeout); err != nil {
		if err := d.client.ContainerKill(context.Background(), d.id, "KILL"); err != nil {
			logrus.Debugf("Unable to stop container %v", err)
		}
	}

	_ = d.client.ContainerRemove(context.Background(), d.id, types.ContainerRemoveOptions{
		RemoveLinks:   true,
		RemoveVolumes: true,
		Force:         true,
	})

	if err := d.client.Close(); err != nil {
		logrus.Debugf("Unable to close docker client %v", err)
	}
}
