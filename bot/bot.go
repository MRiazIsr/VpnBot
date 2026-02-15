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
