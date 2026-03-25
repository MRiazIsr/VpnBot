package bot

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
	tele "gopkg.in/telebot.v3"
)

var AdminID int64 = 0
var ServerIP string

// Делаем переменную Bot глобальной, чтобы main.go мог к ней обращаться
var Bot *tele.Bot

func Start(token string, adminID int64) {
	AdminID = adminID

	ServerIP = os.Getenv("SERVER_IP")
	if ServerIP == "" {
		ServerIP = "49.13.201.110"
	}

	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		log.Fatal(err)
		return
	}

	// Сохраняем экземпляр бота в глобальную переменную
	Bot = b

	// --- Menus ---

	// Главное меню
	menu := &tele.ReplyMarkup{ResizeKeyboard: true}
	btnStatus := menu.Text("📊 Статус")
	btnConnect := menu.Text("🔑 Подключиться")
	btnHelp := menu.Text("🆘 Помощь")
	menu.Reply(menu.Row(btnStatus, btnConnect), menu.Row(btnHelp))

	// Гостевое меню
	guestMenu := &tele.ReplyMarkup{ResizeKeyboard: true}
	btnRequest := guestMenu.Text("📝 Подать заявку")
	btnCheck := guestMenu.Text("🔄 Проверить статус")
	guestMenu.Reply(guestMenu.Row(btnRequest), guestMenu.Row(btnCheck))

	// --- Handlers ---

	checkStatus := func(c tele.Context) error {
		var user database.User
		result := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user)

		if result.Error != nil {
			var existingUser database.User
			if c.Sender().ID == AdminID {
				if err := database.DB.Where("username = 'MRiaz' AND telegram_id = 0").First(&existingUser).Error; err == nil {
					existingUser.TelegramID = c.Sender().ID
					database.DB.Save(&existingUser)
					return c.Send("✅ Ваш профиль администратора успешно привязан!", menu)
				}
			}
			return c.Send("👋 Вы не зарегистрированы в системе.\n\nНажмите **📝 Подать заявку**, чтобы запросить доступ.", guestMenu)
		}

		if user.Status == "banned" {
			return c.Send("⛔ Ваш доступ заблокирован.")
		}

		return c.Send("✅ Выберите действие:", menu)
	}

	b.Handle("/start", checkStatus)
	b.Handle(&btnCheck, checkStatus)

	handleRequest := func(c tele.Context) error {
		var user database.User
		if database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user).Error == nil {
			return c.Send("✅ У вас уже есть доступ!", menu)
		}

		// Красивое имя пользователя в уведомлении админу
		userLink := c.Sender().Username
		if userLink == "" {
			firstName := escapeMarkdown(c.Sender().FirstName)
			userLink = fmt.Sprintf("[%s](tg://user?id=%d)", firstName, c.Sender().ID)
		} else {
			userLink = "@" + escapeMarkdown(userLink)
		}

		msg := fmt.Sprintf("🔔 *Новая заявка!*\nUser: %s\nID: `%d`", userLink, c.Sender().ID)

		approveBtn := &tele.ReplyMarkup{}
		btnApprove := approveBtn.Data("✅ Одобрить", "approve", fmt.Sprintf("%d", c.Sender().ID))
		approveBtn.Inline(approveBtn.Row(btnApprove))

		targetAdmin := AdminID
		if targetAdmin == 0 {
			targetAdmin = 124343839
		}

		_, err := b.Send(&tele.User{ID: targetAdmin}, msg, approveBtn, tele.ModeMarkdown)
		if err != nil {
			log.Println("Ошибка отправки админу:", err)
			return c.Send("❌ Ошибка отправки заявки (не настроен админ).")
		}

		return c.Send("⏳ Заявка отправлена администратору.\nОжидайте уведомления или нажмите **Проверить статус** позже.", guestMenu)
	}

	b.Handle("/request", handleRequest)
	b.Handle(&btnRequest, handleRequest)

	b.Handle(&tele.Btn{Unique: "approve"}, func(c tele.Context) error {
		targetIDStr := c.Data()
		targetID := parseInt(targetIDStr)

		var exists database.User
		if database.DB.Where("telegram_id = ?", targetID).First(&exists).Error == nil {
			return c.Edit("⚠️ Этот пользователь уже добавлен.")
		}

		// 1. Техническое имя (для VLESS конфига) всегда user_ID
		vlessUsername := fmt.Sprintf("user_%d", targetID)

		// 2. Пытаемся узнать реальный юзернейм для админки
		tgUsername := ""
		chat, err := b.ChatByID(targetID)
		if err == nil && chat.Username != "" {
			tgUsername = chat.Username
		}

		newUser := database.User{
			UUID:              uuid.New().String(),
			Username:          vlessUsername,
			TelegramUsername:  tgUsername,
			TelegramID:        targetID,
			Status:            "active",
			TrafficLimit:      30 * 1024 * 1024 * 1024,
			SubscriptionToken: database.GenerateToken(),
		}

		database.DB.Create(&newUser)

		service.GenerateAndReload()
		service.SyncTelemetUsers()
		service.GenerateAndReloadTelemet()

		userChat := &tele.User{ID: targetID}
		b.Send(userChat, "🎉 **Поздравляем! Ваш доступ одобрен.**\n\nТеперь вы можете пользоваться VPN. Нажмите кнопку ниже, чтобы подключиться.", menu)

		return c.Edit(fmt.Sprintf("✅ Пользователь %s (%s) одобрен.", vlessUsername, tgUsername))
	})

	b.Handle(&btnConnect, func(c tele.Context) error {
		var user database.User
		if err := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user).Error; err != nil {
			return c.Send("❌ Пользователь не найден.")
		}

		var inbounds []database.InboundConfig
		database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

		if len(inbounds) == 0 {
			return c.Send("⚠️ Нет доступных подключений.")
		}

		connectMenu := &tele.ReplyMarkup{}
		rows := []tele.Row{}

		// Master subscription button
		btnSub := connectMenu.Data("⭐ Авто-подключение (рекомендуется)", "conn_sub")
		btnSubQR := connectMenu.Data("📷 QR-код", "conn_sub_qr")
		rows = append(rows, connectMenu.Row(btnSub, btnSubQR))

		// Individual inbound buttons
		for _, ib := range inbounds {
			btnLink := connectMenu.Data(fmt.Sprintf("🔗 %s", ib.DisplayName), "conn_link", fmt.Sprintf("%d", ib.ID))
			btnQR := connectMenu.Data(fmt.Sprintf("📷 %s", ib.DisplayName), "conn_qr", fmt.Sprintf("%d", ib.ID))
			rows = append(rows, connectMenu.Row(btnLink, btnQR))
		}
		// Кнопка VK TURN Tunnel (если включён)
		var turnCfg database.TurnConfig
		if database.DB.First(&turnCfg).Error == nil && turnCfg.Enabled && turnCfg.VKJoinLink != "" {
			btnTurn := connectMenu.Data("🌐 VK Tunnel", "conn_turn")
			rows = append(rows, connectMenu.Row(btnTurn))
		}

		// Кнопка Telegram Proxy (если telemt включён)
		var telemetCfg database.TelemetConfig
		if database.DB.First(&telemetCfg).Error == nil && telemetCfg.Enabled {
			btnProxy := connectMenu.Data("📡 Telegram Proxy", "conn_tg_proxy")
			btnProxyQR := connectMenu.Data("📷 QR Proxy", "conn_tg_proxy_qr")
			rows = append(rows, connectMenu.Row(btnProxy, btnProxyQR))
		}

		connectMenu.Inline(rows...)

		text := "🔑 **Подключение к VPN**\n\n" +
			"⭐ **Авто-подключение** — одна ссылка на все серверы.\n" +
			"Приложение само выберет лучший и переключится, если один перестанет работать. " +
			"Также настройки обновляются автоматически — не нужно ничего менять вручную.\n\n" +
			"Ниже — отдельные серверы, если хотите выбрать конкретный."
		return c.Send(text, connectMenu, tele.ModeMarkdown)
	})

	b.Handle(&tele.Btn{Unique: "conn_sub"}, func(c tele.Context) error {
		var user database.User
		if err := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user).Error; err != nil {
			return c.Send("❌ Пользователь не найден.")
		}
		subURL := buildSubURL(user.SubscriptionToken)
		return c.Send(fmt.Sprintf("`%s`", subURL), tele.ModeMarkdown)
	})

	b.Handle(&tele.Btn{Unique: "conn_sub_qr"}, func(c tele.Context) error {
		var user database.User
		if err := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user).Error; err != nil {
			return c.Send("❌ Пользователь не найден.")
		}
		subURL := buildSubURL(user.SubscriptionToken)

		qr, qrErr := qrcode.Encode(subURL, qrcode.Medium, 256)
		if qrErr != nil {
			return c.Send("❌ Ошибка генерации QR кода.")
		}

		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(qr)), Caption: "Авто-подключение — сканируйте в Hiddify"}
		return c.Send(photo)
	})

	b.Handle(&tele.Btn{Unique: "conn_link"}, func(c tele.Context) error {
		ib, user, err := getInboundAndUser(c)
		if err != nil {
			return c.Send(err.Error())
		}
		link := service.GenerateLinkForInbound(ib, user, ServerIP)
		return c.Send(fmt.Sprintf("`%s`", link), tele.ModeMarkdown)
	})

	b.Handle(&tele.Btn{Unique: "conn_qr"}, func(c tele.Context) error {
		ib, user, err := getInboundAndUser(c)
		if err != nil {
			return c.Send(err.Error())
		}
		link := service.GenerateLinkForInbound(ib, user, ServerIP)

		qr, qrErr := qrcode.Encode(link, qrcode.Medium, 256)
		if qrErr != nil {
			return c.Send("❌ Ошибка генерации QR кода.")
		}

		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(qr)), Caption: fmt.Sprintf("%s — сканируйте в Hiddify", ib.DisplayName)}
		return c.Send(photo)
	})

	// Обработчик кнопки Telegram Proxy — ссылка
	b.Handle(&tele.Btn{Unique: "conn_tg_proxy"}, func(c tele.Context) error {
		link, err := getTelemetLink(c)
		if err != nil {
			return c.Send(err.Error())
		}
		return c.Send("📡 Нажмите на ссылку для подключения прокси:\n\n"+link)
	})

	// Обработчик кнопки Telegram Proxy — QR-код
	b.Handle(&tele.Btn{Unique: "conn_tg_proxy_qr"}, func(c tele.Context) error {
		link, err := getTelemetLink(c)
		if err != nil {
			return c.Send(err.Error())
		}

		qr, qrErr := qrcode.Encode(link, qrcode.Medium, 256)
		if qrErr != nil {
			return c.Send("❌ Ошибка генерации QR кода.")
		}

		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(qr)), Caption: "Telegram Proxy — сканируйте камерой Telegram"}
		return c.Send(photo)
	})

	b.Handle(&tele.Btn{Unique: "conn_file"}, func(c tele.Context) error {
		return c.Send("📂 **Файл конфигурации**\n\nРекомендуется использовать **Ссылку** (кнопка выше) или QR-код.\nСсылка позволяет автоматически обновлять настройки при изменениях на сервере, а файл — нет.\n\nПросто скопируйте ссылку и вставьте её в приложение.", tele.ModeMarkdown)
	})

	b.Handle(&btnStatus, func(c tele.Context) error {
		msg, rm := getStatusMsg(c.Sender().ID)
		return c.Send(msg, tele.ModeMarkdown, rm)
	})

	b.Handle(&tele.Btn{Unique: "status_refresh"}, func(c tele.Context) error {
		msg, rm := getStatusMsg(c.Sender().ID)
		return c.Edit(msg, tele.ModeMarkdown, rm)
	})

	b.Handle(&btnHelp, func(c tele.Context) error {
		helpMsg := `📖 **Инструкция по подключению:**

🚀 **Рекомендуемое приложение: Hiddify**
(Работает одинаково на Android и Windows)

🤖 **Android:**
1. Скачайте **Hiddify** (Google Play или GitHub).
2. Скопируйте ссылку в боте (кнопка "Подключиться" -> "Ссылка").
3. Откройте Hiddify -> Нажмите "+" (Новый профиль) -> **Добавить из буфера обмена**.
4. Нажмите большую кнопку подключения.

💻 **Windows:**
1. Скачайте **Hiddify** (GitHub или Microsoft Store).
   *(Если Windows Defender блокирует установку — разрешите запуск)*.
2. Скопируйте ссылку в боте.
3. В приложении нажмите "+" -> **Добавить из буфера обмена**.
4. Внизу выберите режим **"Системный прокси"**.
5. Подключитесь.
   *(В настройках можно включить запуск при загрузке).*

🍏 **iOS (iPhone/iPad):**
1. Скачайте **V2Box** или **Streisand** в AppStore.
2. Скопируйте ссылку в боте.
3. Откройте приложение — оно само предложит добавить конфиг.
4. Если нет: Configs -> "+" -> Import v2ray uri from clipboard.

❓ Если возникли проблемы, пишите администратору.`

		return c.Send(helpMsg, tele.ModeMarkdown)
	})

	// --- VK TURN Tunnel handlers ---

	// Обработчик кнопки VK Tunnel — инструкция для пользователя
	b.Handle(&tele.Btn{Unique: "conn_turn"}, func(c tele.Context) error {
		var cfg database.TurnConfig
		if err := database.DB.First(&cfg).Error; err != nil || !cfg.Enabled || cfg.VKJoinLink == "" {
			return c.Send("❌ VK TURN туннель не настроен.")
		}

		instruction := service.GenerateTurnClientInstruction(ServerIP, cfg)
		return c.Send(instruction, tele.ModeMarkdownV2)
	})

	// /turn — статус TURN-туннеля (только админ)
	b.Handle("/turn", func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}

		var cfg database.TurnConfig
		if err := database.DB.First(&cfg).Error; err != nil {
			return c.Send("⚙️ VK TURN туннель не настроен.\n\nИспользуйте `/turn_setup` для настройки.", tele.ModeMarkdown)
		}

		running := service.IsTurnProxyRunning()
		statusEmoji := "🔴"
		statusText := "Остановлен"
		if running {
			statusEmoji = "🟢"
			statusText = "Работает"
		}

		link := cfg.VKJoinLink
		if link == "" {
			link = "не задана"
		}

		msg := fmt.Sprintf(
			"🌐 *VK TURN Tunnel*\n\n"+
				"%s Статус: *%s*\n"+
				"🔗 VK ссылка: `%s`\n"+
				"🔌 Порт туннеля: `%d`\n"+
				"➡️ Forward порт: `%d`\n"+
				"📡 Потоков: `%d`\n"+
				"📝 %s",
			statusEmoji, statusText,
			link,
			cfg.TunnelPort,
			cfg.ForwardPort,
			cfg.Streams,
			cfg.StatusMsg,
		)

		turnMenu := &tele.ReplyMarkup{}
		rows := []tele.Row{}
		if running {
			btnStop := turnMenu.Data("⏹ Остановить", "turn_stop_btn")
			btnRestart := turnMenu.Data("🔄 Перезапустить", "turn_restart_btn")
			rows = append(rows, turnMenu.Row(btnStop, btnRestart))
		} else {
			btnStart := turnMenu.Data("▶️ Запустить", "turn_start_btn")
			rows = append(rows, turnMenu.Row(btnStart))
		}
		btnTest := turnMenu.Data("🧪 Тест credentials", "turn_test_btn")
		rows = append(rows, turnMenu.Row(btnTest))
		turnMenu.Inline(rows...)

		return c.Send(msg, tele.ModeMarkdown, turnMenu)
	})

	// /turn_setup — полная настройка (только админ)
	b.Handle("/turn_setup", func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}

		// Проверяем есть ли VK токен в env или в аргументе
		vkToken := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/turn_setup"))
		if vkToken == "" {
			vkToken = os.Getenv("VK_TOKEN")
		}

		if vkToken == "" {
			return c.Send(
				"⚙️ *Настройка VK TURN туннеля*\n\n"+
					"Для создания VK-звонков нужен VK токен\\.\n\n"+
					"*Как получить:*\n"+
					"1\\. Зарегистрируйте отдельный VK\\-аккаунт\n"+
					"2\\. Создайте Standalone\\-приложение: `vk.com/apps?act=manage`\n"+
					"3\\. Получите токен:\n"+
					"`https://oauth.vk.com/authorize?client_id=APP_ID&scope=calls&redirect_uri=https://oauth.vk.com/blank.html&response_type=token&v=5.264`\n"+
					"4\\. Скопируйте `access_token` из URL\n\n"+
					"Отправьте: `/turn_setup <ваш_токен>`\n"+
					"Или задайте `VK_TOKEN` в env и повторите `/turn_setup`",
				tele.ModeMarkdownV2,
			)
		}

		c.Send("⏳ Устанавливаю vk-turn-proxy server...")

		// Создаём или обновляем конфиг
		var cfg database.TurnConfig
		if database.DB.First(&cfg).Error != nil {
			cfg = database.TurnConfig{
				Enabled:     true,
				VKToken:     vkToken,
				TunnelPort:  56000,
				ForwardPort: 8444,
				Streams:     16,
			}
			database.DB.Create(&cfg)
		} else {
			cfg.Enabled = true
			cfg.VKToken = vkToken
			database.DB.Save(&cfg)
		}

		// Создаём VK-звонок
		c.Send("📞 Создаю VK-звонок...")
		joinLink, callID, err := service.CreateVKCall(vkToken)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ Ошибка создания VK-звонка: %s\n\nМожете задать ссылку вручную: `/turn_link <url>`", err.Error()), tele.ModeMarkdown)
		}

		cfg.VKJoinLink = joinLink
		cfg.VKCallID = callID
		database.DB.Save(&cfg)

		c.Send(fmt.Sprintf("✅ VK-звонок создан: `%s`", joinLink), tele.ModeMarkdown)

		// Устанавливаем и запускаем сервис
		if err := service.SetupTurnProxy(); err != nil {
			return c.Send(fmt.Sprintf("❌ Ошибка настройки: %s", err.Error()))
		}

		return c.Send("✅ VK TURN туннель настроен и запущен!\n\nТеперь пользователи увидят кнопку \"🌐 VK Tunnel\" в меню подключения.")
	})

	// /turn_link — задать ссылку VK-звонка вручную (только админ)
	b.Handle("/turn_link", func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}

		link := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/turn_link"))
		if link == "" {
			return c.Send("Использование: `/turn_link https://vk.com/call/join/...`", tele.ModeMarkdown)
		}

		if !strings.Contains(link, "vk.com/call/join/") {
			return c.Send("❌ Неверный формат ссылки. Ожидается: `https://vk.com/call/join/...`", tele.ModeMarkdown)
		}

		var cfg database.TurnConfig
		if database.DB.First(&cfg).Error != nil {
			cfg = database.TurnConfig{
				Enabled:     true,
				VKJoinLink:  link,
				TunnelPort:  56000,
				ForwardPort: 8444,
				Streams:     16,
			}
			database.DB.Create(&cfg)
		} else {
			cfg.VKJoinLink = link
			cfg.Enabled = true
			database.DB.Save(&cfg)
		}

		// Тестируем credentials
		c.Send("🧪 Проверяю ссылку...")
		turnServer, err := service.TestTurnCreds(link)
		if err != nil {
			return c.Send(fmt.Sprintf("⚠️ Ссылка сохранена, но тест credentials не прошёл: %s\n\nВозможно, звонок завершён.", err.Error()))
		}

		return c.Send(fmt.Sprintf("✅ Ссылка сохранена и проверена!\nTURN сервер: `%s`", turnServer), tele.ModeMarkdown)
	})

	// /turn_stop — остановить туннель (только админ)
	b.Handle("/turn_stop", func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}

		if err := service.StopTurnProxy(); err != nil {
			return c.Send(fmt.Sprintf("❌ Ошибка: %s", err.Error()))
		}

		var cfg database.TurnConfig
		if database.DB.First(&cfg).Error == nil {
			cfg.Enabled = false
			database.DB.Save(&cfg)
		}

		return c.Send("✅ VK TURN туннель остановлен.")
	})

	// Inline кнопки управления TURN
	b.Handle(&tele.Btn{Unique: "turn_stop_btn"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}
		if err := service.StopTurnProxy(); err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Ошибка: " + err.Error()})
		}
		c.Respond(&tele.CallbackResponse{Text: "Остановлен"})
		// Обновляем сообщение
		return c.Edit("🌐 VK TURN Tunnel\n\n🔴 Статус: Остановлен")
	})

	b.Handle(&tele.Btn{Unique: "turn_start_btn"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}
		if err := service.StartTurnProxy(); err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Ошибка: " + err.Error()})
		}
		c.Respond(&tele.CallbackResponse{Text: "Запущен"})
		return c.Edit("🌐 VK TURN Tunnel\n\n🟢 Статус: Работает")
	})

	b.Handle(&tele.Btn{Unique: "turn_restart_btn"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}
		service.StopTurnProxy()
		if err := service.StartTurnProxy(); err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Ошибка: " + err.Error()})
		}
		c.Respond(&tele.CallbackResponse{Text: "Перезапущен"})
		return c.Edit("🌐 VK TURN Tunnel\n\n🟢 Статус: Работает (перезапущен)")
	})

	b.Handle(&tele.Btn{Unique: "turn_test_btn"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}
		var cfg database.TurnConfig
		if database.DB.First(&cfg).Error != nil || cfg.VKJoinLink == "" {
			return c.Respond(&tele.CallbackResponse{Text: "VK ссылка не задана"})
		}

		c.Respond(&tele.CallbackResponse{Text: "Тестирую..."})
		turnServer, err := service.TestTurnCreds(cfg.VKJoinLink)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ Тест не прошёл: %s", err.Error()))
		}
		return c.Send(fmt.Sprintf("✅ Credentials работают!\nTURN сервер: `%s`", turnServer), tele.ModeMarkdown)
	})

	b.Handle("/broadcast", func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return c.Send("⛔ Только администратор может отправлять рассылку.")
		}

		text := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/broadcast"))
		if text == "" {
			return c.Send("Использование: `/broadcast <текст сообщения>`", tele.ModeMarkdown)
		}

		var users []database.User
		database.DB.Where("telegram_id > 0").Find(&users)

		sent, failed := 0, 0
		for _, u := range users {
			_, err := b.Send(&tele.User{ID: u.TelegramID}, text)
			if err != nil {
				failed++
			} else {
				sent++
			}
		}

		return c.Send(fmt.Sprintf("📨 Рассылка завершена.\n✅ Отправлено: %d\n❌ Ошибок: %d", sent, failed))
	})

	// Фоновая задача
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		for range ticker.C {
			err := service.UpdateTrafficViaAPI()
			if err != nil {
				log.Println("Traffic update error:", err)
			}
		}
	}()

	b.Start()
}

func getInboundAndUser(c tele.Context) (database.InboundConfig, database.User, error) {
	idStr := c.Data()
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return database.InboundConfig{}, database.User{}, fmt.Errorf("❌ Неверный ID инбаунда.")
	}

	var ib database.InboundConfig
	if err := database.DB.First(&ib, id).Error; err != nil {
		return database.InboundConfig{}, database.User{}, fmt.Errorf("❌ Подключение не найдено.")
	}

	var user database.User
	if err := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user).Error; err != nil {
		return database.InboundConfig{}, database.User{}, fmt.Errorf("❌ Пользователь не найден.")
	}

	return ib, user, nil
}

func getStatusMsg(tgID int64) (string, *tele.ReplyMarkup) {
	// 1. ВАЖНО: Сначала читаем статистику через API
	service.UpdateTrafficViaAPI()

	// 2. Получаем данные текущего пользователя
	user := getUser(tgID)
	used := formatBytes(user.TrafficUsed)
	limit := formatBytes(user.TrafficLimit)

	limitStr := limit
	if user.TrafficLimit == 0 {
		limitStr = "∞ (Безлимит)"
	}

	// 3. Считаем ОБЩЕЕ количество пользователей
	var totalUsers int64
	database.DB.Model(&database.User{}).Where("status = ?", "active").Count(&totalUsers)

	// 4. Формируем сообщение
	msg := fmt.Sprintf(
		"📊 **Статус сервера**\n"+
			"👥 Активных пользователей: **%d**\n\n"+
			"👤 **Ваш профиль:** `%s`\n"+
			"📉 Потрачено: **%s**\n"+
			"📈 Лимит: **%s**",
		totalUsers, user.Username, used, limitStr,
	)

	rm := &tele.ReplyMarkup{}
	btnRefresh := rm.Data("🔄 Обновить", "status_refresh")
	rm.Inline(rm.Row(btnRefresh))

	return msg, rm
}

func getUser(tgID int64) database.User {
	var user database.User
	database.DB.Where("telegram_id = ?", tgID).First(&user)
	return user
}

func parseInt(s string) int64 {
	var i int64
	fmt.Sscanf(s, "%d", &i)
	return i
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"`", "\\`",
		"[", "\\[",
	)
	return replacer.Replace(s)
}

func buildSubURL(token string) string {
	domain := os.Getenv("SERVER_DOMAIN")
	if domain != "" {
		return fmt.Sprintf("https://%s/sub/%s", domain, token)
	}
	return fmt.Sprintf("https://%s:8085/sub/%s", ServerIP, token)
}

// getTelemetLink возвращает ссылку tg://proxy для текущего юзера
func getTelemetLink(c tele.Context) (string, error) {
	var user database.User
	if err := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user).Error; err != nil {
		return "", fmt.Errorf("❌ Пользователь не найден.")
	}

	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil || !cfg.Enabled {
		return "", fmt.Errorf("❌ Telegram Proxy не настроен.")
	}

	// Ищем или создаём TelemetUser (атомарно через FirstOrCreate)
	var tu database.TelemetUser
	result := database.DB.Where("user_id = ? AND telemet_config_id = ?", user.ID, cfg.ID).
		Attrs(database.TelemetUser{
			Label:  user.Username,
			Secret: service.GenerateSecret(),
		}).
		FirstOrCreate(&tu)
	if result.Error != nil {
		return "", fmt.Errorf("❌ Ошибка создания секрета прокси.")
	}
	if result.RowsAffected > 0 {
		// Новый секрет создан — перегенерируем конфиг telemt
		service.GenerateAndReloadTelemet()
	}

	serverAddr := cfg.ServerAddress
	if serverAddr == "" {
		serverAddr = ServerIP
	}

	tlsDomain := cfg.TLSDomain
	if tlsDomain == "" {
		tlsDomain = "dl.google.com"
	}

	link := service.GenerateTelemetProxyLink(serverAddr, cfg.Port, tu.Secret, tlsDomain)
	return link, nil
}

func formatBytes(b int64) string {
	if b == 0 {
		return "0.00 MB"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
