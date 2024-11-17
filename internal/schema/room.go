package schema

type CreateRoomInput struct {
	Theme string `json:"theme" binding:"required"`
}

type CreateRoomOutput struct {
	ID string `json:"id"`
}

type GetRoomsOutput struct {
	ID    string `json:"id"`
	Theme string `json:"theme"`
}

type GetRoomByIDInput struct {
	RoomID string `uri:"room_id" binding:"required,uuid"`
}
