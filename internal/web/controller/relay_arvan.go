package controller

import (
	"errors"
	"io"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// The Arvan routes move a front's public IP between the ones its ArvanCloud
// server holds. They sit under /relays, so they are super-admin only as well.

type arvanKeyRequest struct {
	Key string `json:"key"`
}

type arvanIPRequest struct {
	IP string `json:"ip"`
}

func (a *RelayController) initArvanRouter(g *gin.RouterGroup) {
	g.GET("/arvan/key", a.arvanKey)
	g.POST("/arvan/key", a.arvanSetKey)
	g.POST("/arvan/key/clear", a.arvanClearKey)
	g.GET("/arvan/front/:id", a.arvanView)
	g.POST("/arvan/front/:id/detect", a.arvanDetect)
	g.POST("/arvan/front/:id/save", a.arvanSave)
	g.POST("/arvan/front/:id/prepare", a.arvanPrepare)
	g.POST("/arvan/front/:id/refill", a.arvanRefill)
	g.POST("/arvan/front/:id/test", a.arvanTest)
	g.POST("/arvan/front/:id/swap", a.arvanSwap)
	g.POST("/arvan/front/:id/release", a.arvanRelease)
}

func (a *RelayController) arvanKey(c *gin.Context) {
	configured, readable := a.arvanService.KeyStatus()
	jsonObj(c, service.ArvanKeyState{Configured: configured, Readable: readable}, nil)
}

func (a *RelayController) arvanSetKey(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[arvanKeyRequest](c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.relays.arvan.toasts.key"), a.arvanService.SetKey(req.Key))
}

func (a *RelayController) arvanClearKey(c *gin.Context) {
	jsonMsg(c, I18nWeb(c, "pages.relays.arvan.toasts.keyClear"), a.arvanService.ClearKey())
}

func (a *RelayController) arvanView(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.arvan.toasts.view"), err)
		return
	}
	view, err := a.arvanService.View(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.arvan.toasts.view"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *RelayController) arvanDetect(c *gin.Context) {
	msg := I18nWeb(c, "pages.relays.arvan.toasts.detect")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	view, err := a.arvanService.Detect(id)
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	jsonMsgObj(c, msg, view, nil)
}

func (a *RelayController) arvanSave(c *gin.Context) {
	msg := I18nWeb(c, "pages.relays.arvan.toasts.save")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	req, ok := middleware.BindJSONAndValidate[service.ArvanFrontRequest](c)
	if !ok {
		return
	}
	view, err := a.arvanService.Save(id, *req)
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	jsonMsgObj(c, msg, view, nil)
}

// arvanStart answers a long action at once; its outcome lands on the front's
// view, which the page polls while the action is busy.
func (a *RelayController) arvanStart(c *gin.Context, msg string, start func(id int) error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	jsonMsg(c, msg, start(id))
}

func (a *RelayController) arvanPrepare(c *gin.Context) {
	a.arvanStart(c, I18nWeb(c, "pages.relays.arvan.toasts.prepare"), a.arvanService.Prepare)
}

func (a *RelayController) arvanRefill(c *gin.Context) {
	a.arvanStart(c, I18nWeb(c, "pages.relays.arvan.toasts.refill"), a.arvanService.Refill)
}

func (a *RelayController) arvanTest(c *gin.Context) {
	a.arvanStart(c, I18nWeb(c, "pages.relays.arvan.toasts.test"), a.arvanService.Test)
}

// arvanSwap takes an optional {ip}; without one the healthiest reserve is used.
func (a *RelayController) arvanSwap(c *gin.Context) {
	msg := I18nWeb(c, "pages.relays.arvan.toasts.swap")
	var body arvanIPRequest
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonMsg(c, msg, err)
		return
	}
	a.arvanStart(c, msg, func(id int) error { return a.arvanService.Swap(id, body.IP) })
}

func (a *RelayController) arvanRelease(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[arvanIPRequest](c)
	if !ok {
		return
	}
	a.arvanStart(c, I18nWeb(c, "pages.relays.arvan.toasts.release"), func(id int) error { return a.arvanService.Release(id, req.IP) })
}
