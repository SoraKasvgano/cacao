package api

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

type businessError int

func (e businessError) Error() string { return statusMessage[int(e)] }

func writeSucceeded(c *gin.Context, err error) bool {
	if err == nil {
		return true
	}
	var business businessError
	if errors.As(err, &business) {
		setErrorCode(c, int(business))
	} else {
		setErrorCode(c, Unexpected)
	}
	return false
}

// Password work and network changes happen outside the transaction. Account,
// initial policy, and default network either commit together or all roll back.
func createAccount(c *gin.Context, user *model.User, network *model.Net, registration, setupAllowed bool) error {
	interval := registerInterval()
	return storage.WriteContext(c.Request.Context(), func(tx *gorm.DB) error {
		var count int64
		if registration {
			if err := tx.Unscoped().Model(&model.User{}).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				if !setupAllowed {
					return businessError(SetupRequired)
				}
				user.Role = "admin"
			} else {
				var config model.Config
				err := tx.Where("key = ?", "openreg").Take(&config).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if config.Value != "true" {
					return businessError(RegistrationDisabled)
				}
				user.Role = "normal"
			}
			if err := tx.Model(&model.User{}).Where("ip = ? AND role = ? AND created_at > ?", user.IP, "normal", time.Now().Add(-interval)).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return businessError(RegisterTooOften)
			}
		}
		if err := tx.Model(&model.User{}).Where("name = ?", user.Name).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return businessError(UsernameAlreadyTaken)
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		if user.Role == "admin" {
			return model.SetConfigTx(tx, "openreg", "false")
		}
		network.UserID = user.ID
		return tx.Create(network).Error
	})
}
