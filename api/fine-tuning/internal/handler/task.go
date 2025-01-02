package handler

import (
	"bufio"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
)

// createTask
//
//	@Description  This endpoint allows you to create fine tune task
//	@ID			createTask
//	@Tags		task
//	@Router		/task [post]
//	@Param		body	body	model.Task	true	"body"
//	@Success	204		"No Content - success without response body"
func (h *Handler) CreateTask(ctx *gin.Context) {
	var task schema.Task
	if err := task.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind service")
		return
	}
	// if err := h.ctrl.CreateTask(ctx, task); err != nil {
	// 	handleBrokerError(ctx, err, "register service")
	// 	return
	// }

	// Create Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}

	containerConfig := &container.Config{
		Image: "execution-test",
		Cmd: []string{
			"python",
			"/app/finetune.py",
			"--data_path", task.DatasetHash,
			"--tokenizer_path", task.PreTrainedModelHash,
			"--model_path", task.PreTrainedModelHash,
			"--output_dir", "/app/exported_model",
		},
	}

	// Define host configuration for volume mounting
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: "/var/tmp/output",
				Target: "/app/exported_model",
			},
		},
		Runtime: "nvidia",
	}

	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		log.Fatalf("Failed to create container: %v", err)
	}

	containerID := resp.ID
	fmt.Printf("Container %s created successfully\n", containerID)

	// Copy the file to the container
	localFilePath := "../../execution/test_trainer"
	containerFilePath := "/app/"
	err = copyToContainer(cli, ctx, containerID, localFilePath, containerFilePath)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Copied file to container: %s\n", containerFilePath)

	// Run the container handling logic asynchronously
	go handleContainerLifecycle(ctx, cli, containerID)
	
	ctx.Status(http.StatusNoContent)
}

// handleContainerLifecycle starts, waits for, and fetches logs from a container
func handleContainerLifecycle(ctx *gin.Context, cli *client.Client, containerID string) {
	fmt.Printf("Starting container: %s\n", containerID)

	// Start the container
	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		log.Printf("Failed to start container: %v", err)
		return
	}
	fmt.Printf("Container %s started successfully\n", containerID)

	// Wait for the container to finish
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			log.Printf("Error waiting for container: %v", err)
			return
		}
	case <-statusCh:
		fmt.Printf("Container %s has stopped\n", containerID)
	}

	// Fetch logs from the container
	out, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		log.Printf("Failed to fetch logs: %v", err)
		return
	}
	defer out.Close()

	fmt.Println("Container logs:")
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading logs: %v", err)
	}
	// Cleanup: Remove the container
	// if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
	// 	log.Fatalf("Failed to remove container: %v", err)
	// }

	// fmt.Println("Container removed successfully")

}

// Copy a file from the local machine to the container
func copyToContainer(cli *client.Client, ctx *gin.Context, containerID, localFilePath, containerFilePath string) error {
	// Prepare the file for copying
	srcInfo, err := archive.TarWithOptions(localFilePath, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("error preparing file for copying: %w", err)
	}

	// Copy the file to the container
	return cli.CopyToContainer(ctx, containerID, containerFilePath, srcInfo, container.CopyToContainerOptions{
		AllowOverwriteDirWithFile: true,
	})
}

// getTask
//
//	@Description  This endpoint allows you to get task by name
//	@ID			getTask
//	@Tags		task
//	@Router		/task/{id} [get]
//	@Param		taskID	path	string	true	"task ID"
//	@Success	200	{object}	model.Task
func (h *Handler) GetTask(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	task, err := h.ctrl.GetTask(&id)
	if err != nil {
		handleBrokerError(ctx, err, "get task")
		return
	}

	ctx.JSON(http.StatusOK, task)
}
