package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 设备激活状态常量定义。
const (
	// ActivationStatusActive 表示设备处于正常激活状态，可正常连接与交互。
	ActivationStatusActive = "active"
	// ActivationStatusFrozen 表示设备处于冻结状态，暂时禁止连接。
	ActivationStatusFrozen = "frozen"
	// ActivationStatusRevoked 表示设备处于作废/注销状态。
	ActivationStatusRevoked = "revoked"
)

// 哨兵错误定义。
var (
	// ErrActivationNotFound 表示设备激活记录未找到。
	ErrActivationNotFound = errors.New("device activation not found")
	// ErrEmptyDeviceId 表示设备 Device-Id 为空。
	ErrEmptyDeviceId = errors.New("device id cannot be empty")
	// ErrEmptyClientId 表示设备 Client-Id 为空。
	ErrEmptyClientId = errors.New("client id cannot be empty")
	// ErrInvalidActivation 表示设备激活记录对象为 nil 或非法。
	ErrInvalidActivation = errors.New("invalid device activation")
)

// DeviceActivation 映射 device_activation 设备激活关系表。
//
// 业务用途：
// 记录 serial_number 与 Device-Id 的激活对应关系，作为已激活设备的运行态主表。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - serial_number: 设备出厂序列号，全局业务唯一，唯一索引 uk_device_activation_serial_number。
// - device_id: 后端设备标识 Device-Id，普通索引 idx_device_activation_device_id。
// - client_id: 固件/客户端安装实例标识，可为空。
// - activation_status: 激活状态（active）。
// - activated_at: 首次激活时间。
// - created_at: 记录创建时间。
// - updated_at: 记录最近更新时间。
type DeviceActivation struct {
	Id               uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber     string    `gorm:"uniqueIndex:uk_device_activation_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	DeviceId         string    `gorm:"index:idx_device_activation_device_id;column:device_id;size:64;not null" json:"device_id"`
	ClientId         string    `gorm:"column:client_id;size:64" json:"client_id,omitempty"`
	ActivationStatus string    `gorm:"column:activation_status;size:16;not null;default:'active'" json:"activation_status"`
	ActivatedAt      time.Time `gorm:"column:activated_at;not null" json:"activated_at"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// DeviceActivationFilter 定义设备激活关系查询过滤条件。
type DeviceActivationFilter struct {
	SerialNumber     string
	DeviceId         string
	ClientId         string
	ActivationStatus string
	Page             int
	PageSize         int
}

// TableName 指定 DeviceActivation 对应的表名。
func (DeviceActivation) TableName() string {
	return "device_activation"
}

// ActivateDeviceBySerialNumber 完成有序列号设备的激活或重新激活操作。
// 业务流程（在事务中执行）：
//  1. 根据 serialNumber 查询 device_activation 表；
//  2. 若未激活过（记录不存在）：创建新的激活记录（status=active, activated_at=now）；
//  3. 若已激活过（记录已存在）：更新 device_id、client_id、activation_status=active；
//     并删除 device_user_ref 表中该 serialNumber 绑定的旧用户记录与 device_access_token 中的旧 Token（保证重新激活后需重新绑定用户，且旧 Token 立即失效）；
//  4. 将 device_hmac_credential 中该 serialNumber 的凭证状态更新为 activated（若当前为 enabled）。
func (d *Database) ActivateDeviceBySerialNumber(ctx context.Context, serialNumber, deviceId, clientId string) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}
	trimmedDeviceId := strings.TrimSpace(deviceId)
	trimmedClientId := strings.TrimSpace(clientId)

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DeviceActivation
		findErr := tx.Where("serial_number = ?", trimmedSN).Take(&existing).Error
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device activation: %w", findErr)
		}

		now := time.Now()
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			act = DeviceActivation{
				SerialNumber:     trimmedSN,
				DeviceId:         trimmedDeviceId,
				ClientId:         trimmedClientId,
				ActivationStatus: ActivationStatusActive,
				ActivatedAt:      now,
			}
			if err := tx.Create(&act).Error; err != nil {
				return fmt.Errorf("create device activation: %w", err)
			}
		} else {
			updates := map[string]any{
				"activation_status": ActivationStatusActive,
			}
			if trimmedDeviceId != "" {
				updates["device_id"] = trimmedDeviceId
			}
			if trimmedClientId != "" {
				updates["client_id"] = trimmedClientId
			}

			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return fmt.Errorf("update device activation: %w", err)
			}

			if err := tx.Where("serial_number = ?", trimmedSN).Delete(&DeviceUserRef{}).Error; err != nil {
				return fmt.Errorf("delete device user ref on reactivate: %w", err)
			}

			if err := tx.Where("serial_number = ?", trimmedSN).Delete(&DeviceAccessToken{}).Error; err != nil {
				return fmt.Errorf("delete device access token on reactivate: %w", err)
			}

			act = existing
			if trimmedDeviceId != "" {
				act.DeviceId = trimmedDeviceId
			}
			if trimmedClientId != "" {
				act.ClientId = trimmedClientId
			}
			act.ActivationStatus = ActivationStatusActive
		}

		if err := tx.Model(&DeviceHmacCredential{}).
			Where("serial_number = ? AND credential_status = ?", trimmedSN, CredentialStatusEnabled).
			Update("credential_status", CredentialStatusActivated).Error; err != nil {
			return fmt.Errorf("update device hmac credential status on activate: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("activate device: %w", err)
	}

	return &act, nil
}

// IsActive 判断设备激活状态当前是否可用。
func (a *DeviceActivation) IsActive() bool {
	if a == nil {
		return false
	}
	return a.ActivationStatus == ActivationStatusActive
}

// FindDeviceActivationBySerialNumber 根据设备序列号查询激活记录。
func (d *Database) FindDeviceActivationBySerialNumber(ctx context.Context, serialNumber string) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("serial_number = ?", trimmedSN).
		Take(&act).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device activation by serial_number %q: %w", trimmedSN, ErrActivationNotFound)
		}
		return nil, fmt.Errorf("query device activation by serial_number: %w", err)
	}

	return &act, nil
}

// BindDeviceWithSN 在单一数据库事务中原子地完成带序列号设备的激活、用户绑定及 Access Token 写入。
// 业务流程（在单一事务中执行）：
//  1. 激活或更新 device_activation 记录（status=active, device_id, client_id）；
//  2. 插入或更新 device_user_ref 绑定关系（user_id=userId）；
//  3. 从 device_hmac_credential 生产表获取 device_type（若凭证状态为 enabled 则更新为 activated）；
//  4. 插入或更新 device_access_token（access_token=accessToken, device_type=生产表device_type, has_exposed=false, issued_at=now, expires_at=nil, revoked_at=nil）。
//
// 若事务中任一步骤失败，整笔事务全部回滚，保证不出现部分提交。
func (d *Database) BindDeviceWithSN(ctx context.Context, serialNumber, deviceId, clientId, accessToken string, userId uint64) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}
	trimmedToken := strings.TrimSpace(accessToken)
	if trimmedToken == "" {
		return nil, ErrEmptyAccessToken
	}
	if userId == 0 {
		return nil, ErrEmptyUserId
	}
	trimmedDeviceId := strings.TrimSpace(deviceId)
	trimmedClientId := strings.TrimSpace(clientId)

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 激活或更新 device_activation 记录
		var existingAct DeviceActivation
		findActErr := tx.Where("serial_number = ?", trimmedSN).Take(&existingAct).Error
		if findActErr != nil && !errors.Is(findActErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device activation: %w", findActErr)
		}

		now := time.Now()
		if errors.Is(findActErr, gorm.ErrRecordNotFound) {
			act = DeviceActivation{
				SerialNumber:     trimmedSN,
				DeviceId:         trimmedDeviceId,
				ClientId:         trimmedClientId,
				ActivationStatus: ActivationStatusActive,
				ActivatedAt:      now,
			}
			if err := tx.Create(&act).Error; err != nil {
				return fmt.Errorf("create device activation: %w", err)
			}
		} else {
			updates := map[string]any{
				"activation_status": ActivationStatusActive,
			}
			if trimmedDeviceId != "" {
				updates["device_id"] = trimmedDeviceId
			}
			if trimmedClientId != "" {
				updates["client_id"] = trimmedClientId
			}
			if err := tx.Model(&existingAct).Updates(updates).Error; err != nil {
				return fmt.Errorf("update device activation: %w", err)
			}
			act = existingAct
			if trimmedDeviceId != "" {
				act.DeviceId = trimmedDeviceId
			}
			if trimmedClientId != "" {
				act.ClientId = trimmedClientId
			}
			act.ActivationStatus = ActivationStatusActive
		}

		// 2. 插入或更新 device_user_ref 绑定关系
		var existingRef DeviceUserRef
		findRefErr := tx.Where("serial_number = ?", trimmedSN).Take(&existingRef).Error
		if findRefErr != nil && !errors.Is(findRefErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device user ref: %w", findRefErr)
		}

		if errors.Is(findRefErr, gorm.ErrRecordNotFound) {
			newRef := DeviceUserRef{
				SerialNumber: trimmedSN,
				UserId:       userId,
			}
			if err := tx.Create(&newRef).Error; err != nil {
				return fmt.Errorf("create device user ref: %w", err)
			}
		} else if existingRef.UserId != userId {
			if err := tx.Model(&existingRef).Update("user_id", userId).Error; err != nil {
				return fmt.Errorf("update device user ref: %w", err)
			}
		}

		// 3. 从 device_hmac_credential 生产表获取 device_type 并更新凭证状态（若当前为 enabled 则更新为 activated）
		deviceType := "default"
		var cred DeviceHmacCredential
		findCredErr := tx.Where("serial_number = ?", trimmedSN).Take(&cred).Error
		if findCredErr == nil {
			if strings.TrimSpace(cred.DeviceType) != "" {
				deviceType = strings.TrimSpace(cred.DeviceType)
			}
			if cred.CredentialStatus == CredentialStatusEnabled {
				if err := tx.Model(&cred).Update("credential_status", CredentialStatusActivated).Error; err != nil {
					return fmt.Errorf("update device hmac credential status: %w", err)
				}
			}
		} else if !errors.Is(findCredErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device hmac credential on bind: %w", findCredErr)
		}

		// 4. 插入或更新 device_access_token（冗余生产表的 device_type）
		var existingToken DeviceAccessToken
		findTokenErr := tx.Where("serial_number = ?", trimmedSN).Take(&existingToken).Error
		if findTokenErr != nil && !errors.Is(findTokenErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device access token: %w", findTokenErr)
		}

		if errors.Is(findTokenErr, gorm.ErrRecordNotFound) {
			newToken := DeviceAccessToken{
				SerialNumber: trimmedSN,
				AccessToken:  trimmedToken,
				DeviceType:   deviceType,
				HasExposed:   false,
				IssuedAt:     now,
			}
			if err := tx.Create(&newToken).Error; err != nil {
				return fmt.Errorf("create device access token: %w", err)
			}
		} else {
			tokenUpdates := map[string]any{
				"access_token": trimmedToken,
				"device_type":  deviceType,
				"has_exposed":  false,
				"issued_at":    now,
				"expires_at":   nil,
				"revoked_at":   nil,
			}
			if err := tx.Model(&existingToken).Updates(tokenUpdates).Error; err != nil {
				return fmt.Errorf("update device access token: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bind device with sn: %w", err)
	}

	return &act, nil
}

// FindDeviceActivationByDeviceIdAndClientId 根据后端 Device-Id 和 Client-Id 查询最新的激活记录。
func (d *Database) FindDeviceActivationByDeviceIdAndClientId(ctx context.Context, deviceId, clientId string) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedDeviceId := strings.TrimSpace(deviceId)
	if trimmedDeviceId == "" {
		return nil, ErrEmptyDeviceId
	}
	trimmedClientId := strings.TrimSpace(clientId)
	if trimmedClientId == "" {
		return nil, ErrEmptyClientId
	}

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("device_id = ? AND client_id = ?", trimmedDeviceId, trimmedClientId).
		Order("id DESC").
		Take(&act).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device activation by device_id %q and client_id %q: %w", trimmedDeviceId, trimmedClientId, ErrActivationNotFound)
		}
		return nil, fmt.Errorf("query device activation by device_id and client_id: %w", err)
	}

	return &act, nil
}

// ListDeviceActivations 分页查询设备激活记录列表。
func (d *Database) ListDeviceActivations(ctx context.Context, filter DeviceActivationFilter) ([]*DeviceActivation, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&DeviceActivation{})

	if sn := strings.TrimSpace(filter.SerialNumber); sn != "" {
		query = query.Where("serial_number LIKE ?", "%"+sn+"%")
	}
	if did := strings.TrimSpace(filter.DeviceId); did != "" {
		query = query.Where("device_id LIKE ?", "%"+did+"%")
	}
	if cid := strings.TrimSpace(filter.ClientId); cid != "" {
		query = query.Where("client_id LIKE ?", "%"+cid+"%")
	}
	if status := strings.TrimSpace(filter.ActivationStatus); status != "" {
		query = query.Where("activation_status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count device activations: %w", err)
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}

	offset := (page - 1) * pageSize
	var acts []*DeviceActivation
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&acts).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query device activations list: %w", err)
	}

	return acts, total, nil
}

// UpdateDeviceActivation 更新指定 Id 的设备激活记录字段。
func (d *Database) UpdateDeviceActivation(ctx context.Context, id uint64, updates map[string]any) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidActivation
	}
	if len(updates) == 0 {
		return nil
	}

	allowedUpdates := make(map[string]any)
	if val, ok := updates["device_id"]; ok {
		allowedUpdates["device_id"] = strings.TrimSpace(fmt.Sprintf("%v", val))
	}
	if val, ok := updates["client_id"]; ok {
		allowedUpdates["client_id"] = strings.TrimSpace(fmt.Sprintf("%v", val))
	}
	if val, ok := updates["activation_status"]; ok {
		allowedUpdates["activation_status"] = strings.TrimSpace(fmt.Sprintf("%v", val))
	}
	if len(allowedUpdates) == 0 {
		return nil
	}
	allowedUpdates["updated_at"] = time.Now()

	res := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("id = ?", id).
		Updates(allowedUpdates)
	if res.Error != nil {
		return fmt.Errorf("update device activation: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrActivationNotFound
	}
	return nil
}

// DeleteDeviceActivation 删除指定 Id 的设备激活记录。
func (d *Database) DeleteDeviceActivation(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidActivation
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&DeviceActivation{})
	if res.Error != nil {
		return fmt.Errorf("delete device activation: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrActivationNotFound
	}
	return nil
}

// BatchDeleteDeviceActivations 批量删除指定 Id 的设备激活记录。
func (d *Database) BatchDeleteDeviceActivations(ctx context.Context, ids []uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if len(ids) == 0 {
		return nil
	}

	validIds := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			validIds = append(validIds, id)
		}
	}
	if len(validIds) == 0 {
		return nil
	}

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIds).Delete(&DeviceActivation{}).Error; err != nil {
		return fmt.Errorf("batch delete device activations: %w", err)
	}
	return nil
}
