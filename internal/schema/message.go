package schema

type Message struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

type CreateMessageInput struct {
	Message string `json:"message" binding:"required"`
}

type CreateMessageOutput struct {
	ID string `json:"id"`
}

type MessageCreatedEvent struct {
	ID      string
	Message string
}

type MessageReactionCountChangedEvent struct {
	ID    string
	Count int64
}

type MessageAnsweredEvent struct {
	ID string
}

type GetMessageOutput struct {
	ID            string `json:"id"`
	Message       string `json:"theme"`
	ReactionCount int64  `json:"reaction_count"`
	Answered      bool   `json:"answered"`
}

type GetMessageByIDInput struct {
	RoomID    string `uri:"room_id" binding:"required,uuid"`
	MessageID string `uri:"message_id" binding:"required,uuid"`
}

type ReactToMessageOutput struct {
	Count int64 `json:"count"`
}
