package database

import (
	"context"
	"errors"
	"testing"
)

func TestDeviceAgentRefCRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Initial Upsert
	ref1, err := db.UpsertDeviceAgentRef(ctx, "robot-dog", 101)
	if err != nil {
		t.Fatalf("UpsertDeviceAgentRef failed: %v", err)
	}
	if ref1.ID == 0 {
		t.Errorf("expected non-zero ID, got %d", ref1.ID)
	}
	if ref1.DeviceType != "robot-dog" {
		t.Errorf("expected device_type robot-dog, got %s", ref1.DeviceType)
	}
	if ref1.AgentConfigID != 101 {
		t.Errorf("expected agent_config_id 101, got %d", ref1.AgentConfigID)
	}

	// 2. Find by device_type
	found, err := db.FindDeviceAgentRefByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("FindDeviceAgentRefByDeviceType failed: %v", err)
	}
	if found.ID != ref1.ID || found.AgentConfigID != 101 {
		t.Errorf("expected found ID=%d agent_config_id=101, got ID=%d agent_config_id=%d",
			ref1.ID, found.ID, found.AgentConfigID)
	}

	// 3. Upsert to update existing device_type
	updated, err := db.UpsertDeviceAgentRef(ctx, "robot-dog", 202)
	if err != nil {
		t.Fatalf("UpsertDeviceAgentRef update failed: %v", err)
	}
	if updated.ID != ref1.ID {
		t.Errorf("expected same ID=%d, got %d", ref1.ID, updated.ID)
	}
	if updated.AgentConfigID != 202 {
		t.Errorf("expected updated agent_config_id 202, got %d", updated.AgentConfigID)
	}

	// Verify update took effect in DB
	foundAfterUpdate, err := db.FindDeviceAgentRefByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("FindDeviceAgentRefByDeviceType after update failed: %v", err)
	}
	if foundAfterUpdate.AgentConfigID != 202 {
		t.Errorf("expected DB agent_config_id 202, got %d", foundAfterUpdate.AgentConfigID)
	}

	// 4. Add another device type with the same agent_config_id
	_, err = db.UpsertDeviceAgentRef(ctx, "smart-speaker", 202)
	if err != nil {
		t.Fatalf("UpsertDeviceAgentRef smart-speaker failed: %v", err)
	}

	// 5. Find by agent_config_id
	refsByAgent, err := db.FindDeviceAgentRefsByAgentConfigID(ctx, 202)
	if err != nil {
		t.Fatalf("FindDeviceAgentRefsByAgentConfigID failed: %v", err)
	}
	if len(refsByAgent) != 2 {
		t.Fatalf("expected 2 refs for agent 202, got %d", len(refsByAgent))
	}

	// 6. List all
	list, err := db.ListDeviceAgentRefs(ctx)
	if err != nil {
		t.Fatalf("ListDeviceAgentRefs failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 refs in list, got %d", len(list))
	}

	// 7. Delete by device_type
	err = db.DeleteDeviceAgentRefByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("DeleteDeviceAgentRefByDeviceType failed: %v", err)
	}

	// 8. Verify deleted
	_, err = db.FindDeviceAgentRefByDeviceType(ctx, "robot-dog")
	if !errors.Is(err, ErrDeviceAgentRefNotFound) {
		t.Errorf("expected ErrDeviceAgentRefNotFound after delete, got %v", err)
	}

	listAfterDelete, err := db.ListDeviceAgentRefs(ctx)
	if err != nil {
		t.Fatalf("ListDeviceAgentRefs after delete failed: %v", err)
	}
	if len(listAfterDelete) != 1 {
		t.Errorf("expected 1 ref remaining, got %d", len(listAfterDelete))
	}
}

func TestDeviceAgentRefValidation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Empty device type in Upsert
	_, err := db.UpsertDeviceAgentRef(ctx, "   ", 101)
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Zero agent config id in Upsert
	_, err = db.UpsertDeviceAgentRef(ctx, "robot", 0)
	if !errors.Is(err, ErrInvalidAgentConfigID) {
		t.Errorf("expected ErrInvalidAgentConfigID, got %v", err)
	}

	// Empty device type in Find
	_, err = db.FindDeviceAgentRefByDeviceType(ctx, "")
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Zero agent config id in FindByAgentConfigID
	_, err = db.FindDeviceAgentRefsByAgentConfigID(ctx, 0)
	if !errors.Is(err, ErrInvalidAgentConfigID) {
		t.Errorf("expected ErrInvalidAgentConfigID, got %v", err)
	}

	// Empty device type in Delete
	err = db.DeleteDeviceAgentRefByDeviceType(ctx, " ")
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Delete non-existent device type
	err = db.DeleteDeviceAgentRefByDeviceType(ctx, "non-existent")
	if !errors.Is(err, ErrDeviceAgentRefNotFound) {
		t.Errorf("expected ErrDeviceAgentRefNotFound, got %v", err)
	}

	// Nil DB checks
	var nilDB *Database
	_, err = nilDB.UpsertDeviceAgentRef(ctx, "robot", 101)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.FindDeviceAgentRefByDeviceType(ctx, "robot")
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.FindDeviceAgentRefsByAgentConfigID(ctx, 101)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.ListDeviceAgentRefs(ctx)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	err = nilDB.DeleteDeviceAgentRefByDeviceType(ctx, "robot")
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}

func TestDeviceAgentRefUniqueConstraint(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Direct Create with duplicate device_type should violate unique constraint
	ref1 := &DeviceAgentRef{
		DeviceType:    "unique-speaker",
		AgentConfigID: 1,
	}
	if err := db.DB().WithContext(ctx).Create(ref1).Error; err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	ref2 := &DeviceAgentRef{
		DeviceType:    "unique-speaker",
		AgentConfigID: 2,
	}
	err := db.DB().WithContext(ctx).Create(ref2).Error
	if err == nil {
		t.Fatal("expected error on duplicate device_type create, got nil")
	}
}
