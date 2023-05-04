package web

import (
	"context"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"log"
	"nsfw_sherlock/common"
	"nsfw_sherlock/utils"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func StartWebServer() {
	// Dry run for TF
	test, err := common.TestPictureNSFW("./pic.jpg")
	if err != nil {
		utils.WrapErrorLog(err.Error())
		return
	}
	utils.ReportSuccess(fmt.Sprintf("NSFW PIC: %v", test))

	//Load bad words
	err = common.LoadBadWords()
	if err != nil {
		utils.WrapErrorLog("Can't load bad words")
		return
	}

	app := fiber.New(fiber.Config{
		AppName:       "NSFW Detector API",
		StrictRouting: false,
		WriteTimeout:  time.Second * 240,
		ReadTimeout:   time.Second * 240,
		IdleTimeout:   time.Second * 240,
	})
	app.Use(cors.New())

	app.Post("/pic/check", picCheck)
	app.Get("/ping", ping)

	go func() {
		err := app.Listen(fmt.Sprintf(":%d", 4000))
		if err != nil {
			log.Fatalln(err.Error())
		}
	}()
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	//go getTransaction()
	log.Println("<- Started NSFW API ->")
	<-c
	_, cancel := context.WithTimeout(context.Background(), time.Second*15)
	log.Println("/// = = Shutting down = = ///")
	defer cancel()
	_ = app.Shutdown()
	os.Exit(0)
}
func ping(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status": "OK",
	})
}

func picCheck(c *fiber.Ctx) error {
	type Req struct {
		Base64   string `json:"base64"`
		Filename string `json:"filename"`
	}
	var req Req
	err := c.BodyParser(&req)
	if err != nil {
		return utils.ReportError(c, err.Error()+"Bad Request", fiber.StatusBadRequest)
	}
	if req.Base64 == "" {
		return utils.ReportError(c, "Bad Request", fiber.StatusBadRequest)
	}
	decoded, err := utils.DecodePayload([]byte(req.Base64))
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
	}
	suffix := strings.Split(req.Filename, ".")[1]
	if _, err := os.Stat("/assets/temp"); os.IsNotExist(err) {
		err := os.Mkdir("/assets/temp", os.ModePerm)
		if err != nil {
			return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
		}
	}
	filename := fmt.Sprintf("./assets/temp/%s.%s", utils.GenerateSecureToken(8), suffix)
	err = os.WriteFile(filename, decoded, 0644)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
	}
	defer func() {
		err := os.Remove(filename)
		if err != nil {
			log.Println(err.Error())
		}
	}()
	isSafe, err := common.TestPictureNSFW(filename)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
	}

	return c.JSON(fiber.Map{
		"status":  "ok",
		"message": "success",
		"isSafe":  isSafe,
	})
}
