package model

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/siyuan-note/dejavu/cloud"
	"github.com/siyuan-note/dejavu/entity"
)

type IrohCloud struct {
	*cloud.BaseCloud
	node *IrohSwarmNode
}

func NewIrohCloud(baseConf *cloud.Conf, node *IrohSwarmNode) *IrohCloud {
	return &IrohCloud{
		BaseCloud: &cloud.BaseCloud{Conf: baseConf},
		node:      node,
	}
}

// UploadBytes writes data to the Iroh Document
func (i *IrohCloud) UploadBytes(filePath string, data []byte, overwrite bool) (int64, error) {
	slog.Info("IrohCloud.UploadBytes", "key", filePath, "size", len(data))
	err := i.node.Set(filePath, data)
	if err != nil {
		slog.Error("IrohCloud.UploadBytes FAILED", "key", filePath, "err", err)
		return 0, err
	}
	// Verify the write round-trips
	readBack, readErr := i.node.Get(filePath)
	if readErr != nil {
		slog.Error("IrohCloud.UploadBytes VERIFY FAILED - can't read back", "key", filePath, "err", readErr)
	} else {
		slog.Info("IrohCloud.UploadBytes VERIFIED", "key", filePath, "readSize", len(readBack))
	}
	return int64(len(data)), nil
}

// UploadObject reads a file from disk and uploads to the Iroh Document
func (i *IrohCloud) UploadObject(filePath string, overwrite bool) (int64, error) {
	fullPath := filepath.Join(i.BaseCloud.Conf.RepoPath, filePath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		slog.Error("IrohCloud.UploadObject disk read failed", "key", filePath, "diskPath", fullPath, "err", err)
		return 0, err
	}
	slog.Info("IrohCloud.UploadObject", "key", filePath, "diskPath", fullPath, "size", len(data))
	return i.UploadBytes(filePath, data, overwrite)
}

// DownloadObject reads data from the Iroh Document
func (i *IrohCloud) DownloadObject(filePath string) ([]byte, error) {
	data, err := i.node.Get(filePath)
	if err != nil {
		slog.Warn("IrohCloud.DownloadObject NOT FOUND", "key", filePath, "err", err)
		return nil, cloud.ErrCloudObjectNotFound
	}
	slog.Info("IrohCloud.DownloadObject OK", "key", filePath, "size", len(data))
	return data, nil
}

// RemoveObject removes a key from the Iroh Document
func (i *IrohCloud) RemoveObject(filePath string) error {
	return i.node.Delete(filePath)
}

// ListObjects lists keys in the Iroh Document matching prefix
func (i *IrohCloud) ListObjects(pathPrefix string) (map[string]*entity.ObjectInfo, error) {
	keys, err := i.node.List(pathPrefix)
	if err != nil {
		return nil, err
	}
	
	objInfos := make(map[string]*entity.ObjectInfo)
	for _, k := range keys {
		// Mock stat info
		objInfos[k] = &entity.ObjectInfo{
			Path: k,
			Size: 0,
		}
	}
	return objInfos, nil
}

// GetRepos returns default repo since Iroh Document IS the repo
func (i *IrohCloud) GetRepos() ([]*cloud.Repo, int64, error) {
	repos := []*cloud.Repo{
		{Name: "main", Size: 0, Updated: ""},
	}
	return repos, 0, nil
}

func (i *IrohCloud) CreateRepo(name string) error {
	return nil
}

func (i *IrohCloud) RemoveRepo(name string) error {
	return nil
}

// Locking mechanism using Priority Leader
func (i *IrohCloud) Lock() error {
	return i.node.RequestLock()
}

func (i *IrohCloud) Unlock() error {
	return i.node.ReleaseLock()
}

// Implement mock for other required interfaces
func (i *IrohCloud) GetIndexes(page int) ([]*entity.Index, int, int, error) {
	data, err := i.node.Get("_siyuan_indexes.json")
	if err != nil {
		return nil, 0, 0, cloud.ErrCloudObjectNotFound
	}
	var indexes []*entity.Index
	if err := json.Unmarshal(data, &indexes); err != nil {
		return nil, 0, 0, err
	}
	// Pagination mock
	return indexes, 1, len(indexes), nil
}
