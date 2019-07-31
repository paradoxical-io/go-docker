package docker

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCanStartContainer(t *testing.T) {
	RequireDocker(t)

	container, err := StartContainer(NewContainerRequest{
		Image: "redis",
		Ports: []int{6379},
	}, t.Name())
	assert.NoError(t, err)
	defer container.Close()

	err = container.WaitForPortToOpen(container.PortMapping(6379), 10*time.Second)
	assert.NoError(t, err)
}

func ExampleStartContainer() {
	// In a "real" test, you'd first want to call RequireDocker(t), to ensure
	// that the test is running on a Docker-able machine:
	//
	// RequireDocker(t)

	// Specify which container you want to start and which ports/env var you need to use.
	// Note that the image name can, and SHOULD contain the version tag (4.0.11 in this case),
	// so that a known image is used, and that tests are reproducible over time.
	//
	// This request specifies that we want to map the standard redis port (6379)
	// to a random open port on the host machine.
	cr := NewContainerRequest{
		Image: "redis:4.0.11",
		EnvVars: map[string]string{
			"FOO": "BAR",
		},
		Ports: []int{6379},
	}

	// start up the container, using a prefix that helps us distinguish this
	// container from the other that might be running on the machine.
	container, err := StartContainer(cr, "example-redis")
	if err != nil {
		panic(fmt.Errorf("failed to start the prerequisite redis Docker container: %s", err.Error()))
	}

	// Wait for the message that redis prints when the server is up.
	err = container.WaitForLogLine("Ready to accept connections", 10*time.Second)
	if err != nil {
		panic(err)
	}

	// Don't forget to close the container when we're done with the test!
	defer container.Close()

	// Find out which local port the container's port 6379 got mapped to
	localPort := container.PortMapping(6379)
	fmt.Printf("so, apparently redis is up on local port: %d\n", localPort)

	// connect to the above port, and do stuff with redis!
}
