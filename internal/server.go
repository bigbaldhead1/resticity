package internal

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/goccy/go-json"
	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/thoas/go-funk"
)

type client struct {
	LastSeen time.Time
}

var clients = make(map[*websocket.Conn]client)
var register = make(chan *websocket.Conn)
var broadcast = make(chan string)
var unregister = make(chan *websocket.Conn)

func runHub() {
	for {
		select {
		case connection := <-register:
			clients[connection] = client{LastSeen: time.Now()}
			log.Debug(
				"connection registered",
				"addr",
				connection.RemoteAddr().String(),
				"clients",
				len(clients),
			)

		case message := <-broadcast:

			for connection := range clients {
				if err := connection.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
					log.Error("write error:", err)

					unregister <- connection
					connection.WriteMessage(websocket.CloseMessage, []byte{})
					connection.Close()
				} else {
					log.Debug("message sent", "addr", connection.RemoteAddr().String(), "msg", message)
				}
			}

		case connection := <-unregister:

			delete(clients, connection)
			log.Debug(
				"connection unregistered",
				"addr",
				connection.RemoteAddr().String(),
				"clients",
				len(clients),
			)

		}
	}
}

func cleanClients() {
	for {
		time.Sleep(1 * time.Second)
		for connection, client := range clients {
			if time.Since(client.LastSeen) > 2*time.Second {

				unregister <- connection

			}
		}
	}
}

func handlePing(c *websocket.Conn) {
	for {
		time.Sleep(1 * time.Second)
		_, _, err := c.ReadMessage()
		if err == nil {
			go func() {
				for connection, client := range clients {

					if connection.RemoteAddr().String() == c.RemoteAddr().String() {
						c := client
						c.LastSeen = time.Now()
						clients[connection] = c

						break
					}
				}
			}()

		}
	}

}

func RunServer(
	scheduler *Scheduler,
	restic *Restic,
	settings *Settings,

	outputChan *chan ChanMsg,
	errorChan *chan ChanMsg,
) {
	server := fiber.New()
	server.Use(cors.New())
	server.Static("/", "./public")

	api := server.Group("/api")

	api.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			c.Locals("allowed", true)
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	go runHub()
	go cleanClients()

	api.Get("/ws", websocket.New(func(c *websocket.Conn) {

		outs := []WsMsg{}
		errs := []WsMsg{}

		defer func() {
			unregister <- c
			c.Close()
		}()

		register <- c

		go handlePing(c)

		for {
			select {
			case o := <-*outputChan:
				m := WsMsg{Id: o.Id, Out: o.Msg, Err: ""}
				log.Debug(m)
				if m.Id != "" {
					if funk.Find(
						outs,
						func(arrm WsMsg) bool { return arrm.Id == m.Id },
					) == nil {
						outs = append(outs, m)
					} else {
						for i, arrm := range outs {
							if arrm.Id == m.Id {
								(outs)[i] = m
								break
							}
						}
					}
				}
				if j, err := json.Marshal(funk.Filter(outs, func(o WsMsg) bool { return o.Out != "" && o.Out != "{}" })); err == nil {
					broadcast <- string(j)

				} else {
					log.Error("socket: marshal", "err", err)
				}
				break
			case e := <-*errorChan:
				m := WsMsg{Id: e.Id, Out: "", Err: e.Msg}
				log.Debug(m)
				if m.Id != "" {
					if funk.Find(
						errs,
						func(arrm WsMsg) bool { return arrm.Id == m.Id },
					) == nil {
						errs = append(errs, m)
					} else {
						for i, arrm := range errs {
							if arrm.Id == m.Id {
								(errs)[i] = m
								break
							}
						}
					}
				}
				if j, err := json.Marshal(funk.Filter(errs, func(o WsMsg) bool { return o.Err != "" && o.Err != "{}" })); err == nil {
					broadcast <- string(j)

				} else {
					log.Error("socket: marshal", "err", err)
				}
				break
			}

		}

	}))

	api.Get("/path/autocomplete", func(c *fiber.Ctx) error {
		paths := []string{}
		path := c.Query("path")
		if files, err := os.ReadDir(path); err == nil {
			for _, f := range files {
				if f.IsDir() {
					paths = append(paths, f.Name())
				}
			}
		} else {
			log.Error("reading path", "path", path, "err", err.Error())
		}
		return c.JSON(paths)
	})

	api.Get("/schedules/:id/:action", func(c *fiber.Ctx) error {
		switch c.Params("action") {
		case "run":
			scheduler.RunJobById(c.Params("id"))
			break
		case "stop":
			scheduler.StopJobById(c.Params("id"))
			break
		}

		return c.SendString(c.Params("action") + " schedule in the background")
	})

	api.Post("/check", func(c *fiber.Ctx) error {
		var r Repository
		if err := c.BodyParser(&r); err != nil {
			c.SendStatus(500)
			return c.SendString(err.Error())
		}

		if r.Type == "local" {
			files, err := os.ReadDir(r.Path)
			if err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			}
			if len(files) > 0 {
				if _, err := restic.Exec(r, []string{"cat", "config"}, []string{}); err != nil {
					c.SendStatus(500)
					return c.SendString(err.Error())
				} else {
					return c.SendString("OK_REPO_EXISTING")
				}
			}
		}

		if r.Type == "s3" || r.Type == "gcs" || r.Type == "azure" {
			if _, err := restic.Exec(r, []string{"cat", "config"}, []string{}); err != nil {
				if strings.Contains(err.Error(), "key does not exist") {
					return c.SendString("OK_REPO_EMPTY")
				}
				c.SendStatus(500)
				return c.SendString(err.Error())
			} else {
				return c.SendString("OK_REPO_EXISTING")
			}
		}

		return c.SendString("OK_REPO_EMPTY")
	})
	api.Post("/init", func(c *fiber.Ctx) error {
		var r Repository
		if err := c.BodyParser(&r); err != nil {
			c.SendStatus(500)
			return c.SendString(err.Error())
		}
		if _, err := restic.Exec(r, []string{"init"}, []string{}); err != nil {
			c.SendStatus(500)
			return c.SendString(err.Error())
		}
		return c.SendString("OK")
	})

	config := api.Group("/config")
	backups := api.Group("/backups")
	config.Get("/", func(c *fiber.Ctx) error {
		settings.Refresh()
		return c.JSON(settings.Config)
	})
	config.Post("/", func(c *fiber.Ctx) error {

		s := new(Config)
		if err := c.BodyParser(s); err != nil {
			c.SendStatus(500)
			return c.SendString(err.Error())
		}
		settings.Save(*s)
		scheduler.RescheduleBackups()
		return c.SendString("OK")
	})

	repositories := api.Group("/repositories")

	repositories.Post("/:id/:action", func(c *fiber.Ctx) error {
		act := c.Params("action")

		switch act {
		case "mount":
			var data MountData
			if err := c.BodyParser(&data); err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			}

			go func(id string) {
				restic.Exec(
					*settings.GetRepositoryById(id),
					[]string{act, data.Path},
					[]string{},
				)
			}(c.Params("id"))

			return c.SendString("OK")
		case "unmount":
			var data MountData
			if err := c.BodyParser(&data); err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			}

			e := exec.Command("/usr/bin/umount", "-l", data.Path)
			e.Output()

			return c.SendString("OK")
		case "snapshots":
			groupBy := c.Query("group_by")
			if groupBy == "" {
				groupBy = "host"
			}
			res, err := restic.Exec(
				*settings.GetRepositoryById(c.Params("id")),
				[]string{act, "--group-by", groupBy},
				[]string{},
			)
			if err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			}
			var data []SnapshotGroup

			if err := json.Unmarshal([]byte(res), &data); err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			}
			return c.JSON(data)
		}

		return c.SendString("Unknown action")

	})

	repositories.Post("/:id/snapshots/:snapshot_id/:action", func(c *fiber.Ctx) error {
		switch c.Params("action") {
		case "browse":
			var data BrowseData
			if err := c.BodyParser(&data); err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			}
			res, err := restic.BrowseSnapshot(
				*settings.GetRepositoryById(c.Params("id")),
				c.Params("snapshot_id"),
				data.Path,
			)
			if err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())

			}
			return c.JSON(res)

		case "restore":
			var data RestoreData
			if err := c.BodyParser(&data); err != nil {
				c.SendStatus(500)
				return c.SendString(err.Error())
			} else {
				if _, err := restic.Exec(
					*settings.GetRepositoryById(c.Params("id")),
					[]string{"restore",
						c.Params("snapshot_id") + ":" + data.RootPath,
						"--target",
						data.ToPath,
						"--include", data.FromPath}, []string{},
				); err != nil {
					c.SendStatus(500)
					return c.SendString(err.Error())
				}
				return c.SendString("OK")
			}
		}

		return c.SendString(c.Params("action"))
	})

	backups.Get("/", func(c *fiber.Ctx) error {

		return c.SendString("Hello, World!")
	})

	server.Listen("0.0.0.0:11278")
}
