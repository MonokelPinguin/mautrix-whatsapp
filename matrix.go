// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"strings"

	"maunium.net/go/gomatrix"
	"maunium.net/go/maulogger"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix-whatsapp/types"
)

type MatrixHandler struct {
	bridge *Bridge
	as     *appservice.AppService
	log    maulogger.Logger
	cmd    *CommandHandler
}

func NewMatrixHandler(bridge *Bridge) *MatrixHandler {
	handler := &MatrixHandler{
		bridge: bridge,
		as:     bridge.AppService,
		log:    bridge.Log.Sub("Matrix"),
		cmd:    NewCommandHandler(bridge),
	}
	bridge.EventProcessor.On(gomatrix.EventMessage, handler.HandleMessage)
	bridge.EventProcessor.On(gomatrix.StateMember, handler.HandleMembership)
	bridge.EventProcessor.On(gomatrix.StateRoomName, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(gomatrix.StateRoomAvatar, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(gomatrix.StateTopic, handler.HandleRoomMetadata)
	return handler
}

func (mx *MatrixHandler) HandleBotInvite(evt *gomatrix.Event) {
	intent := mx.as.BotIntent()

	user := mx.bridge.GetUser(evt.Sender)
	if user == nil {
		return
	}

	resp, err := intent.JoinRoom(evt.RoomID, "", nil)
	if err != nil {
		mx.log.Debugln("Failed to join room", evt.RoomID, "with invite from", evt.Sender)
		return
	}

	members, err := intent.JoinedMembers(resp.RoomID)
	if err != nil {
		mx.log.Debugln("Failed to get members in room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if len(members.Joined) < 2 {
		mx.log.Debugln("Leaving empty room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if !user.Whitelisted {
		intent.SendNotice(resp.RoomID, "You are not whitelisted to use this bridge.\n"+
			"If you're the owner of this bridge, see the bridge.permissions section in your config file.")
		intent.LeaveRoom(resp.RoomID)
		return
	}

	hasPuppets := false
	for mxid, _ := range members.Joined {
		if mxid == intent.UserID || mxid == evt.Sender {
			continue
		} else if _, _, ok := mx.bridge.ParsePuppetMXID(types.MatrixUserID(mxid)); ok {
			hasPuppets = true
			continue
		}
		mx.log.Debugln("Leaving multi-user room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.SendNotice(resp.RoomID, "This bridge is user-specific, please don't invite me into rooms with other users.")
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if !hasPuppets {
		user := mx.bridge.GetUser(types.MatrixUserID(evt.Sender))
		user.SetManagementRoom(types.MatrixRoomID(resp.RoomID))
		intent.SendNotice(string(user.ManagementRoom), "This room has been registered as your bridge management/status room.")
		mx.log.Debugln(resp.RoomID, "registered as a management room with", evt.Sender)
	}
}

func (mx *MatrixHandler) HandleMembership(evt *gomatrix.Event) {
	if evt.Content.Membership == "invite" && evt.GetStateKey() == mx.as.BotMXID() {
		mx.HandleBotInvite(evt)
	}
}

func (mx *MatrixHandler) HandleRoomMetadata(evt *gomatrix.Event) {
	user := mx.bridge.GetUser(types.MatrixUserID(evt.Sender))
	if user == nil || !user.Whitelisted {
		return
	}

	portal := user.GetPortalByMXID(evt.RoomID)
	if portal == nil || portal.IsPrivateChat() {
		return
	}

	var resp <-chan string
	var err error
	switch evt.Type {
	case gomatrix.StateRoomName:
		resp, err = user.Conn.UpdateGroupSubject(evt.Content.Name, portal.JID)
	case gomatrix.StateRoomAvatar:
		return
	case gomatrix.StateTopic:
		return
	}
	if err != nil {
		mx.log.Errorln(err)
	} else {
		out := <-resp
		mx.log.Infoln(out)
	}
}

func (mx *MatrixHandler) HandleMessage(evt *gomatrix.Event) {
	roomID := types.MatrixRoomID(evt.RoomID)
	user := mx.bridge.GetUser(types.MatrixUserID(evt.Sender))

	if !user.Whitelisted {
		return
	}

	if evt.Content.MsgType == gomatrix.MsgText {
		commandPrefix := mx.bridge.Config.Bridge.CommandPrefix
		hasCommandPrefix := strings.HasPrefix(evt.Content.Body, commandPrefix)
		if hasCommandPrefix {
			evt.Content.Body = strings.TrimLeft(evt.Content.Body[len(commandPrefix):], " ")
		}
		if hasCommandPrefix || roomID == user.ManagementRoom {
			mx.cmd.Handle(roomID, user, evt.Content.Body)
			return
		}
	}

	portal := user.GetPortalByMXID(roomID)
	if portal != nil {
		portal.HandleMatrixMessage(evt)
	}
}
