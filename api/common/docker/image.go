package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

// ImageInfo represents information about a Docker image
type ImageInfo struct {
	Image   string    `json:"image"`
	ImageID string    `json:"imageId"`
	Digest  string    `json:"digest"` // Image digest (e.g., sha256:abc123...)
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
}

// GetImageInfo returns information about a Docker image including its digest
func GetImageInfo(ctx context.Context, cli *client.Client, imageName string) (*ImageInfo, error) {
	inspect, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect image %s: %v", imageName, err)
	}

	created, _ := time.Parse(time.RFC3339Nano, inspect.Created)

	return &ImageInfo{
		Image:   imageName,
		ImageID: inspect.ID,
		Digest:  digestFor(imageName, inspect.RepoDigests),
		Created: created,
		Size:    inspect.Size,
	}, nil
}

// digestFor picks the digest RepoDigests records for imageName's repository.
//
// The daemon carries one entry per repository the image is known under
// (["ghcr.io/0gfoundation/0g-serving-broker@sha256:abc…", …]) ordered by the
// normalized reference, so the first entry belongs to whichever repository sorts
// first. For an image pulled from a mirror as well, that is not the repository
// the caller asked about, and the digest reported would name an image under a
// name the caller never mentioned.
func digestFor(imageName string, repoDigests []string) string {
	for _, entry := range repoDigests {
		if repo, digest, ok := strings.Cut(entry, "@"); ok && repo == repoOf(imageName) {
			return digest
		}
	}

	// No entry for that repository. Two ways to get here: the image was addressed by ID
	// (repoOf of "sha256:<hex>" matches no repository, so this is that path's ONLY
	// behaviour), or the daemon normalized the name to something else.
	//
	// One entry is unambiguous — that is the image's only known repository, so its digest is
	// the answer. More than one is not, and answering with the first would reinstate exactly
	// what the loop above exists to remove: the entry that happens to sort first, which for
	// an image known under both an origin and a mirror can be the mirror's, with a different
	// manifest digest. That digest now decides which key signs responses, so a wrong answer
	// makes the derived address disagree with the RTMR3 record. Refusing puts the failure at
	// the lookup, where it can be explained.
	if len(repoDigests) == 1 {
		if _, digest, ok := strings.Cut(repoDigests[0], "@"); ok {
			return digest
		}
	}
	return ""
}

// repoOf strips any tag or digest from a reference.
//
// Only a colon in the last path segment is a tag separator; an earlier one is a
// port on the registry host, as in "localhost:5000/broker".
func repoOf(imageName string) string {
	repo, _, _ := strings.Cut(imageName, "@")
	lastSegment := repo[strings.LastIndex(repo, "/")+1:]
	if tag := strings.Index(lastSegment, ":"); tag != -1 {
		repo = repo[:len(repo)-len(lastSegment)+tag]
	}
	return repo
}

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
