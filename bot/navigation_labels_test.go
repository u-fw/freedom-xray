package main

import "testing"

// Every dashboard link uses the same "Main menu" label, and screens listed on
// the Tools card can return to it.
func TestNavigationButtonsSayWhereTheyGo(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	home := tr("en", "menu.home")
	for _, data := range []string{"menu_tools", "menu_help", "menu_stats", "menu_users", "menu_nodes", "menu_status", "menu_traffic", "menu_routing"} {
		s.handleCallback(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 5, Chat: chat}, Data: data})
		_, keyboard := capture.card(t)
		toTools := false
		for _, row := range keyboard.InlineKeyboard {
			for _, button := range row {
				if button.Data == "menu_main" && button.Text != home {
					t.Errorf("%s: dashboard button labelled %q", data, button.Text)
				}
				toTools = toTools || button.Data == "menu_tools"
			}
		}
		if (data == "menu_help" || data == "menu_stats") && !toTools {
			t.Errorf("%s: no way back to Tools", data)
		}
	}
}
