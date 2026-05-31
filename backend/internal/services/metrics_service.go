package services

import (
	"context"
	"encoding/json"


	"github.com/moby/moby/client"

	"github.com/thirzq/dockerledger/internal/docker"
)


func (s *ContainerService) GetContainerStats(ctx context.Context, containerID string) (interface{}, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return nil, err
    }

    // Use ContainerStatsOptions with stream = false (one-shot)
    statsResult, err := cli.ContainerStats(ctx, containerID, client.ContainerStatsOptions{
        Stream: false, // return a single object, not a stream
    })
    if err != nil {
        return nil, err
    }
    defer statsResult.Body.Close()

    // Decode the JSON stats into a generic map/interface
    var stats interface{}
    if err := json.NewDecoder(statsResult.Body).Decode(&stats); err != nil {
        return nil, err
    }

    return stats, nil
}