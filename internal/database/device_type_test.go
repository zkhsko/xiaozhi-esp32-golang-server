package database

import (
	"context"
	"errors"
	"testing"
)

func TestDeviceTypeCRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Initial Upsert
	dt1, err := db.UpsertDeviceType(ctx, "robot-dog", 101)
	if err != nil {
		t.Fatalf("UpsertDeviceType failed: %v", err)
	}
	if dt1.ID == 0 {
		t.Errorf("expected non-zero ID, got %d", dt1.ID)
	}
	if dt1.DeviceType != "robot-dog" {
		t.Errorf("expected device_type robot-dog, got %s", dt1.DeviceType)
	}
	if dt1.AgentConfigID != 101 {
		t.Errorf("expected agent_config_id 101, got %d", dt1.AgentConfigID)
	}

	// 2. Find by device_type
	found, err := db.FindDeviceTypeByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("FindDeviceTypeByDeviceType failed: %v", err)
	}
	if found.ID != dt1.ID || found.AgentConfigID != 101 {
		t.Errorf("expected found ID=%d agent_config_id=101, got ID=%d agent_config_id=%d",
			dt1.ID, found.ID, found.AgentConfigID)
	}

	// 3. Upsert to update existing device_type
	updated, err := db.UpsertDeviceType(ctx, "robot-dog", 202)
	if err != nil {
		t.Fatalf("UpsertDeviceType update failed: %v", err)
	}
	if updated.ID != dt1.ID {
		t.Errorf("expected same ID=%d, got %d", dt1.ID, updated.ID)
	}
	if updated.AgentConfigID != 202 {
		t.Errorf("expected updated agent_config_id 202, got %d", updated.AgentConfigID)
	}

	// Verify update took effect in DB
	foundAfterUpdate, err := db.FindDeviceTypeByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("FindDeviceTypeByDeviceType after update failed: %v", err)
	}
	if foundAfterUpdate.AgentConfigID != 202 {
		t.Errorf("expected DB agent_config_id 202, got %d", foundAfterUpdate.AgentConfigID)
	}

	// 4. Add another device type with the same agent_config_id
	_, err = db.UpsertDeviceType(ctx, "smart-speaker", 202)
	if err != nil {
		t.Fatalf("UpsertDeviceType smart-speaker failed: %v", err)
	}

	// 5. Find by agent_config_id
	typesByAgent, err := db.FindDeviceTypesByAgentConfigID(ctx, 202)
	if err != nil {
		t.Fatalf("FindDeviceTypesByAgentConfigID failed: %v", err)
	}
	if len(typesByAgent) != 2 {
		t.Fatalf("expected 2 types for agent 202, got %d", len(typesByAgent))
	}

	// 6. List all
	list, err := db.ListDeviceTypes(ctx)
	if err != nil {
		t.Fatalf("ListDeviceTypes failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 types in list, got %d", len(list))
	}

	// 7. Delete by device_type
	err = db.DeleteDeviceTypeByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("DeleteDeviceTypeByDeviceType failed: %v", err)
	}

	// 8. Verify deleted
	_, err = db.FindDeviceTypeByDeviceType(ctx, "robot-dog")
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound after delete, got %v", err)
	}

	listAfterDelete, err := db.ListDeviceTypes(ctx)
	if err != nil {
		t.Fatalf("ListDeviceTypes after delete failed: %v", err)
	}
	if len(listAfterDelete) != 1 {
		t.Errorf("expected 1 type remaining, got %d", len(listAfterDelete))
	}
}

func TestDeviceTypeValidation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Empty device type in Upsert
	_, err := db.UpsertDeviceType(ctx, "   ", 101)
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Zero agent config id in Upsert
	_, err = db.UpsertDeviceType(ctx, "robot", 0)
	if !errors.Is(err, ErrInvalidAgentConfigID) {
		t.Errorf("expected ErrInvalidAgentConfigID, got %v", err)
	}

	// Empty device type in Find
	_, err = db.FindDeviceTypeByDeviceType(ctx, "")
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Zero agent config id in FindByAgentConfigID
	_, err = db.FindDeviceTypesByAgentConfigID(ctx, 0)
	if !errors.Is(err, ErrInvalidAgentConfigID) {
		t.Errorf("expected ErrInvalidAgentConfigID, got %v", err)
	}

	// Empty device type in Delete
	err = db.DeleteDeviceTypeByDeviceType(ctx, " ")
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Delete non-existent device type
	err = db.DeleteDeviceTypeByDeviceType(ctx, "non-existent")
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound, got %v", err)
	}

	// Nil DB checks
	var nilDB *Database
	_, err = nilDB.UpsertDeviceType(ctx, "robot", 101)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.FindDeviceTypeByDeviceType(ctx, "robot")
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.FindDeviceTypesByAgentConfigID(ctx, 101)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.ListDeviceTypes(ctx)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	err = nilDB.DeleteDeviceTypeByDeviceType(ctx, "robot")
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}

func TestDeviceTypeUniqueConstraint(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Direct Create with duplicate device_type should violate unique constraint
	dt1 := &DeviceType{
		DeviceType:    "unique-speaker",
		AgentConfigID: 1,
	}
	if err := db.DB().WithContext(ctx).Create(dt1).Error; err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	dt2 := &DeviceType{
		DeviceType:    "unique-speaker",
		AgentConfigID: 2,
	}
	err := db.DB().WithContext(ctx).Create(dt2).Error
	if err == nil {
		t.Fatal("expected error on duplicate device_type create, got nil")
	}
}
