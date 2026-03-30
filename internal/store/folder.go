package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jarvis/internal/model"

	"gopkg.in/yaml.v3"
)

func folderPath(id string) string {
	return filepath.Join(JarvisHome(), "folders", id+".yaml")
}

func SaveFolder(f *model.Folder) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal folder: %w", err)
	}
	return WriteAtomic(folderPath(f.ID), data)
}

func GetFolder(id string) (*model.Folder, error) {
	data, err := os.ReadFile(folderPath(id))
	if err != nil {
		return nil, fmt.Errorf("read folder %s: %w", id, err)
	}
	var f model.Folder
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("unmarshal folder %s: %w", id, err)
	}
	return &f, nil
}

func DeleteFolder(id string) error {
	return os.Remove(folderPath(id))
}

func ListFolders() ([]*model.Folder, error) {
	base := filepath.Join(JarvisHome(), "folders")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var folders []*model.Folder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".yaml")
		f, err := GetFolder(id)
		if err != nil {
			continue
		}
		folders = append(folders, f)
	}
	return folders, nil
}
