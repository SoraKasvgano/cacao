package api

import (
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/lanthora/cacao/background"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
)

var backgroundTasks atomic.Pointer[background.Manager]

func SetBackgroundTasks(manager *background.Manager) { backgroundTasks.Store(manager) }

func AdminBackgroundTasks(c *gin.Context) {
	tasks := []background.Status{}
	if manager := backgroundTasks.Load(); manager != nil {
		tasks = manager.Snapshot()
	}
	setResponseData(c, gin.H{"tasks": tasks, "storage": storage.Stats(), "pendingDevices": model.PendingDeviceStateCount()})
}

func AdminRunBackgroundTask(c *gin.Context) {
	var request struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&request) != nil || request.Name == "" {
		setErrorCode(c, InvalidRequest)
		return
	}
	triggerBackgroundTask(c, request.Name)
}

func triggerBackgroundTask(c *gin.Context, name string) {
	manager := backgroundTasks.Load()
	if manager == nil {
		setErrorCode(c, Unexpected)
		return
	}
	if err := manager.Trigger(name); err != nil {
		setResponse(c, InvalidRequest, err.Error(), nil)
		return
	}
	setResponseData(c, gin.H{"accepted": true, "name": name})
}
