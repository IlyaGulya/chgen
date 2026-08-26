package apiinventory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Load reads and validates an inventory file.
func Load(path string) (Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Inventory{}, err
	}
	var inventory Inventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return Inventory{}, fmt.Errorf("decode inventory: %w", err)
	}
	if err := inventory.Validate(); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}
