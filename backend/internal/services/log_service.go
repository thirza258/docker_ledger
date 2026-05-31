package services

import (
	"fmt"
	"io"
	"strings"
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"

	"github.com/moby/moby/client"

	"github.com/thirzq/dockerledger/internal/docker"
)

func (s *ContainerService) GetContainerLogs(ctx context.Context, containerID string, tail int) (string, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return "", err
    }

    // Set up log options
    tailStr := fmt.Sprintf("%d", tail)
    options := client.ContainerLogsOptions{
        ShowStdout: true,
        ShowStderr: true,
        Tail:       tailStr,
        Timestamps: false, // you can make this configurable if needed
    }

    // Get logs as a readable stream
    logsReader, err := cli.ContainerLogs(ctx, containerID, options)
    if err != nil {
        return "", err
    }
    defer logsReader.Close()

    // Read all logs into a string
    logBytes, err := io.ReadAll(logsReader)
    if err != nil {
        return "", err
    }

    // Docker multiplexes stdout/stderr with 8-byte headers; we need to strip them.
    // The header format: [8]byte{STREAM_TYPE, 0, 0, 0, SIZE1, SIZE2, SIZE3, SIZE4}
    // We'll use a simple helper to extract just the message.
    return decodeDockerLogs(logBytes), nil
}

// decodeDockerLogs strips the 8-byte Docker multiplexing headers from raw log data.
func decodeDockerLogs(raw []byte) string {
    var messages []string
    i := 0
    for i < len(raw) {
        if i+8 > len(raw) {
            break
        }
        // Skip the 8-byte header
        msgLen := int(raw[i+7]) | int(raw[i+6])<<8 | int(raw[i+5])<<16 | int(raw[i+4])<<24
        i += 8
        if i+msgLen > len(raw) {
            break
        }
        messages = append(messages, string(raw[i:i+msgLen]))
        i += msgLen
    }
    return strings.Join(messages, "")
}

func (s *ContainerService) StreamContainerLogs(ctx context.Context, containerID string, tail int) (io.ReadCloser, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return nil, err
    }

    tailStr := fmt.Sprintf("%d", tail)
    options := client.ContainerLogsOptions{
        ShowStdout: true,
        ShowStderr: true,
        Follow:     true, 
        Tail:       tailStr,
        Timestamps: false,
    }

    return cli.ContainerLogs(ctx, containerID, options)
}

type ContainerInfo struct {
    ID   string
    Name string
}

func (s *ContainerService) GetAllRunningContainers(ctx context.Context) ([]ContainerInfo, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return nil, err
    }

    // ContainerList returns client.ContainerListResult with an Items field
    result, err := cli.ContainerList(ctx, client.ContainerListOptions{All: false})
    if err != nil {
        return nil, err
    }

    infos := make([]ContainerInfo, 0, len(result.Items))
    for _, c := range result.Items {
        name := strings.TrimPrefix(c.Names[0], "/")
        infos = append(infos, ContainerInfo{ID: c.ID, Name: name})
    }
    return infos, nil
}

// FollowContainerLogs streams logs from a container and sends JSON lines.
func (s *ContainerService) FollowContainerLogs(ctx context.Context, containerID, containerName string) (<-chan string, context.CancelFunc, error) {
    cli, err := docker.GetClient()
    if err != nil {
        return nil, nil, err
    }

    streamCtx, cancel := context.WithCancel(ctx)

    opts := client.ContainerLogsOptions{
        ShowStdout: true,
        ShowStderr: true,
        Follow:     true,
        Tail:       "0",
        Timestamps: false,
    }

    logsReader, err := cli.ContainerLogs(streamCtx, containerID, opts)
    if err != nil {
        cancel()
        return nil, nil, err
    }

    output := make(chan string, 100)

    go func() {
        defer close(output)
        defer logsReader.Close()

        // Decode the multiplexed stream into plain lines
        decoder := newMultiplexedDecoder(logsReader)
        scanner := bufio.NewScanner(decoder)
        for scanner.Scan() {
            line := scanner.Text()
            msg := map[string]string{
                "container": containerName,
                "message":   line,
            }
            jsonBytes, _ := json.Marshal(msg)
            select {
            case output <- string(jsonBytes):
            case <-streamCtx.Done():
                return
            }
        }
    }()

    return output, cancel, nil
}

// multiplexedDecoder strips the 8‑byte Docker headers from a stream.
type multiplexedDecoder struct {
    r io.Reader
    buf []byte
}

func newMultiplexedDecoder(r io.Reader) *multiplexedDecoder {
    return &multiplexedDecoder{r: r}
}

func (d *multiplexedDecoder) Read(p []byte) (int, error) {
    // If we have leftover data from previous decode, serve it first
    if len(d.buf) > 0 {
        n := copy(p, d.buf)
        d.buf = d.buf[n:]
        return n, nil
    }

    // Read the 8‑byte header
    header := make([]byte, 8)
    _, err := io.ReadFull(d.r, header)
    if err != nil {
        return 0, err
    }

    // The last 4 bytes of the header are the payload length (big endian)
    msgLen := binary.BigEndian.Uint32(header[4:8])
    if msgLen == 0 {
        // No data, continue to next header
        return d.Read(p)
    }

    // Read the actual log message
    msg := make([]byte, msgLen)
    _, err = io.ReadFull(d.r, msg)
    if err != nil {
        return 0, err
    }

    // Store the message in the buffer
    d.buf = msg
    n := copy(p, d.buf)
    d.buf = d.buf[n:]
    return n, nil
}

