package api

import (
	"ask-me-anything/internal/schema"
	"ask-me-anything/internal/store/pgstore"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"sync"
)

const (
	KindMessageCreated           = "message_created"
	KindMessageReactionIncreased = "message_reaction_increased"
	KindMessageReactionDecreased = "message_reaction_decreased"
	KindMessageAnswered          = "message_answered"
)

type Handler struct {
	queries     *pgstore.Queries
	router      *gin.Engine
	upgrader    websocket.Upgrader
	subscribers map[string]map[*websocket.Conn]context.CancelFunc
	mu          *sync.Mutex
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func NewHandler(q *pgstore.Queries) http.Handler {
	router := gin.New()

	handler := &Handler{
		queries:     q,
		router:      router,
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc),
		mu:          &sync.Mutex{},
	}

	router.Use(gin.Recovery(), gin.Logger())
	router.Use(corsMiddleware())

	v1 := router.Group("/v1")

	v1.GET("/subscribe/:room_id", handler.handleSubscribe)

	rooms := v1.Group("/rooms")
	{
		rooms.POST("/", handler.handleCreateRoom)
		rooms.GET("/", handler.handleGetRooms)

		rooms.GET("/:room_id", handler.handleGetRoom)
	}

	messages := rooms.Group("/:room_id/messages")
	{
		messages.POST("/", handler.handleCreateRoomMessage)
		messages.GET("/", handler.handleGetRoomMessages)

		messages.GET("/:message_id", handler.handleGetRoomMessage)
		messages.PATCH("/:message_id/react", handler.handleReactToMessage)
		messages.DELETE("/:message_id/react", handler.handleRemoveReactionFromMessage)
		messages.PATCH("/:message_id/answer", handler.handleMarkMessageAsAnswered)
	}

	return handler
}

func (h *Handler) notifyClients(msg schema.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subscribers, ok := h.subscribers[msg.RoomID]
	if !ok || len(subscribers) == 0 {
		return
	}

	for conn, cancel := range subscribers {
		if err := conn.WriteJSON(msg); err != nil {
			slog.Error("failed to send message to client", "error", err)
			cancel()
		}
	}
}

func (h *Handler) handleSubscribe(c *gin.Context) {
	var uri schema.GetRoomByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Warn("failed to upgrade connection", "error", err)
		c.PureJSON(http.StatusBadRequest, "something went wrong")
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(c.Request.Context())

	h.mu.Lock()
	if _, ok := h.subscribers[roomID.String()]; !ok {
		h.subscribers[roomID.String()] = make(map[*websocket.Conn]context.CancelFunc)
	}

	slog.Info("new client connected", "room_id", roomID.String(), "client_ip", c.ClientIP())
	h.subscribers[roomID.String()][conn] = cancel
	h.mu.Unlock()

	<-ctx.Done()

	h.mu.Lock()
	delete(h.subscribers[roomID.String()], conn)
	h.mu.Unlock()
}

func (h *Handler) handleCreateRoom(c *gin.Context) {
	var body schema.CreateRoomInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID, err := h.queries.InsertRoom(c.Request.Context(), body.Theme)
	if err != nil {
		slog.Error("failed to insert room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	c.PureJSON(http.StatusCreated, schema.CreateRoomOutput{ID: roomID.String()})
}

func (h *Handler) handleGetRooms(c *gin.Context) {
	rooms, err := h.queries.GetRooms(c.Request.Context())
	if err != nil {
		slog.Error("failed to get rooms", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	if rooms == nil {
		c.PureJSON(http.StatusOK, []schema.GetRoomsOutput{})
		return
	}

	var output []schema.GetRoomsOutput
	for _, room := range rooms {
		output = append(output, schema.GetRoomsOutput{ID: room.ID.String(), Theme: room.Theme})
	}

	c.PureJSON(http.StatusOK, output)
}

func (h *Handler) handleGetRoom(c *gin.Context) {
	var uri schema.GetRoomByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	room, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	c.PureJSON(http.StatusOK, schema.GetRoomsOutput{ID: room.ID.String(), Theme: room.Theme})
}

func (h *Handler) handleCreateRoomMessage(c *gin.Context) {
	var uri schema.GetRoomByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	var body schema.CreateMessageInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	messageID, err := h.queries.InsertMessage(c.Request.Context(), pgstore.InsertMessageParams{
		RoomID:  roomID,
		Message: body.Message,
	})
	if err != nil {
		slog.Error("failed to insert message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	go h.notifyClients(schema.Message{
		Kind:   KindMessageCreated,
		RoomID: roomID.String(),
		Value: schema.MessageCreatedEvent{
			ID:      messageID.String(),
			Message: body.Message,
		},
	})

	c.PureJSON(http.StatusCreated, schema.CreateMessageOutput{ID: messageID.String()})
}

func (h *Handler) handleGetRoomMessages(c *gin.Context) {
	var uri schema.GetRoomByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	messages, err := h.queries.GetRoomMessages(c.Request.Context(), roomID)
	if err != nil {
		slog.Error("failed to get room messages", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	if messages == nil {
		c.PureJSON(http.StatusOK, []schema.GetMessageOutput{})
		return
	}

	var output []schema.GetMessageOutput
	for _, message := range messages {
		output = append(output, schema.GetMessageOutput{
			ID:            message.ID.String(),
			Message:       message.Message,
			ReactionCount: message.ReactionCount,
			Answered:      message.Answered,
		})
	}

	c.PureJSON(http.StatusOK, output)
}

func (h *Handler) handleGetRoomMessage(c *gin.Context) {
	var uri schema.GetMessageByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	messageID := uuid.MustParse(uri.MessageID)
	message, err := h.queries.GetMessage(c.Request.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "message not found")
			return
		}

		slog.Error("failed to get message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	c.PureJSON(http.StatusOK, schema.GetMessageOutput{
		ID:            messageID.String(),
		Message:       message.Message,
		ReactionCount: message.ReactionCount,
		Answered:      message.Answered,
	})
}

func (h *Handler) handleReactToMessage(c *gin.Context) {
	var uri schema.GetMessageByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	messageID := uuid.MustParse(uri.MessageID)
	_, err = h.queries.GetMessage(c.Request.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "message not found")
			return
		}

		slog.Error("failed to get message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	count, err := h.queries.ReactToMessage(c.Request.Context(), messageID)
	if err != nil {
		slog.Error("failed to react to message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	go h.notifyClients(schema.Message{
		Kind:   KindMessageReactionIncreased,
		RoomID: roomID.String(),
		Value: schema.MessageReactionCountChangedEvent{
			ID:    messageID.String(),
			Count: count,
		},
	})

	c.PureJSON(http.StatusOK, schema.ReactToMessageOutput{
		Count: count,
	})
}

func (h *Handler) handleRemoveReactionFromMessage(c *gin.Context) {
	var uri schema.GetMessageByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	messageID := uuid.MustParse(uri.MessageID)
	_, err = h.queries.GetMessage(c.Request.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "message not found")
			return
		}

		slog.Error("failed to get message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	count, err := h.queries.RemoveReactionFromMessage(c.Request.Context(), messageID)
	if err != nil {
		slog.Error("failed to remove reaction from message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	go h.notifyClients(schema.Message{
		Kind:   KindMessageReactionDecreased,
		RoomID: roomID.String(),
		Value: schema.MessageReactionCountChangedEvent{
			ID:    messageID.String(),
			Count: count,
		},
	})

	c.PureJSON(http.StatusOK, schema.ReactToMessageOutput{Count: count})
}

func (h *Handler) handleMarkMessageAsAnswered(c *gin.Context) {
	var uri schema.GetMessageByIDInput
	if err := c.ShouldBindUri(&uri); err != nil {
		c.PureJSON(http.StatusBadRequest, err.Error())
		return
	}

	roomID := uuid.MustParse(uri.RoomID)
	_, err := h.queries.GetRoom(c.Request.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "room not found")
			return
		}

		slog.Error("failed to get room", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	messageID := uuid.MustParse(uri.MessageID)
	_, err = h.queries.GetMessage(c.Request.Context(), messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.PureJSON(http.StatusNotFound, "message not found")
			return
		}

		slog.Error("failed to get message", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	if err := h.queries.MarkMessageAsAnswered(c.Request.Context(), messageID); err != nil {
		slog.Error("failed to mark message as answered", "error", err)
		c.PureJSON(http.StatusInternalServerError, "something went wrong")
		return
	}

	go h.notifyClients(schema.Message{
		Kind:   KindMessageAnswered,
		RoomID: roomID.String(),
		Value: schema.MessageAnsweredEvent{
			ID: messageID.String(),
		},
	})

	c.PureJSON(http.StatusOK, nil)
}
