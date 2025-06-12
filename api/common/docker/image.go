package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

func ImageExists(ctx context.Context, cli *client.Client, imageName string) (bool, error) {
	images, err := cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to list images: %v", err)
	}

	for _, image := range images {
		for _, tag := range image.RepoTags {
			if tag == imageName {
				return true, nil
			}
		}
	}

	return false, nil
}
func ImageBuild(ctx context.Context, cli *client.Client, buildDirectory, tag string, logger log.Logger) error {
	tar, err := archive.TarWithOptions(buildDirectory, &archive.TarOptions{})
	if err != nil {
		return err
	}
	defer tar.Close()

	buildOptions := types.ImageBuildOptions{
		Dockerfile: "Dockerfile",  // Name of the Dockerfile
		Tags:       []string{tag}, // Tag for the image
		Remove:     true,          // Remove intermediate containers after build
	}

	buildResponse, err := cli.ImageBuild(ctx, tar, buildOptions)
	if err != nil {
		return err
	}
	defer buildResponse.Body.Close()

	decoder := json.NewDecoder(buildResponse.Body)
	var buildError error = nil

	for {
		var message struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}

		if err := decoder.Decode(&message); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if message.Error != "" {
			buildError = fmt.Errorf("build failed: %s", message.Error)
		} else if message.ErrorDetail.Message != "" {
			buildError = fmt.Errorf("build failed: %s", message.ErrorDetail.Message)
		}

		if message.Stream != "" {
			logger.Debug(message.Stream)
		}
	}

	return buildError
}

func PullImage(ctx context.Context, cli *client.Client, expectImag string, pull bool) error {
	imageExists, err := ImageExists(ctx, cli, expectImag)
	if err != nil {
		return err
	}

	if !imageExists {
		if pull {
			out, err := cli.ImagePull(ctx, expectImag, image.PullOptions{})
			if err != nil {
				return fmt.Errorf("failed to pull Docker image %s: %v", expectImag, err)
			}
			defer out.Close()
			io.Copy(os.Stdout, out)
		} else {
			return fmt.Errorf("failed to found image: %v", expectImag)
		}
	}

	return nil
}
