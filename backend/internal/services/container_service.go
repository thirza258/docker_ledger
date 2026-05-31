package services

import (
	"context"

	"github.com/moby/moby/client"

	"github.com/thirzq/dockerledger/internal/docker"
	"github.com/thirzq/dockerledger/internal/models"

)

type ContainerService struct{}

func NewContainerService() *ContainerService {
	return &ContainerService{}
}

func (s *ContainerService) IsDockerConnected(ctx context.Context) bool {
	err := docker.Ping(ctx)
	return err == nil
}

func (s *ContainerService) ListContainers(ctx context.Context) ([]models.DockerContainer, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return nil, err
    }

    containers, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
    if err != nil {
        return nil, err
    }

    result := make([]models.DockerContainer, 0, len(containers.Items))  // ✅ len(containers.Items)
    for _, c := range containers.Items {                               // ✅ iterate over .Items
        result = append(result, models.DockerContainer{
            Id:     c.ID,
            Names:  c.Names,
            State:  string(c.State),    // ✅ convert to string
            Image:  c.Image,
            Status: c.Status,
        })
    }
    return result, nil
}

func (s *ContainerService) GetContainerByID(ctx context.Context, containerID string) (interface{}, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return nil, err
    }
    return cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
}
// Future methods: ListContainers, StartContainer, etc.