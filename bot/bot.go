package bot

import (
	"bytes"
	"fmt"
	"log"
	"time"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
	tele "gopkg.in/telebot.v3"
)

var AdminID int64 = 0

func Start(token string, adminID int64) {
	AdminID = adminID
	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		log.Fatal(err)
		return
	}

	// --- Menus ---
	menu := &tele.ReplyMarkup{ResizeKeyboard: true}
	btnStatus := menu.Text("📊 Статус")
	btnConnect := menu.Text("🔑 Подключиться")
	btnHelp := menu.Text("🆘 Помощь")
	menu.Reply(menu.Row(btnStatus, btnConnect), menu.Row(btnHelp))

	// Кнопки формата подключения
	connectMenu := &tele.ReplyMarkup{}
	btnLink := connectMenu.Data("🔗 Ссылка", "conn_link")
	btnFile := connectMenu.Data("📁 Файл конфига", "conn_file")
	btnQR := connectMenu.Data("📷 QR код", "conn_qr")
	connectMenu.Inline(
		connectMenu.Row(btnLink),
		connectMenu.Row(btnFile, btnQR),
	)

	// --- Handlers ---

	b.Handle("/start", func(c tele.Context) error {
		var user database.User
		// Ищем по TelegramID
		result := database.DB.Where("telegram_id = ?", c.Sender().ID).First(&user)

		// Если не нашли по Telegram ID
		if result.Error != nil {
			var existingUser database.User
			// Логика привязки админа
			if c.Sender().ID == AdminID || c.Sender().ID == 124343839 {
				if err := database.DB.Where("username = 'MRiaz' AND telegram_id = 0").First(&existingUser).Error; err == nil {
					existingUser.TelegramID = c.Sender().ID
					database.DB.Save(&existingUser)
					return c.Send("Ваш профиль администратора (MRiaz) успешно привязан!", menu)
				}
			}

			return c.Send("Вы не зарегистрированы. Нажмите /request для заявки.")
		}

		if user.Status == "banned" {
			return c.Send("⛔ Ваш доступ заблокирован.")
		}

		return c.Send("Меню управления VPN", menu)
	})

	// Заявка на доступ
	b.Handle("/request", func(c tele.Context) error {
		msg := fmt.Sprintf("🔔 **Новая заявка!**\nUser: @%s (%d)", c.Sender().Username, c.Sender().ID)

		approveBtn := &tele.ReplyMarkup{}
		btnApprove := approveBtn.Data("✅ Одобрить", "approve", fmt.Sprintf("%d", c.Sender().ID))
		approveBtn.Inline(approveBtn.Row(btnApprove))

		targetAdmin := AdminID
		if targetAdmin == 0 {
			targetAdmin = 124343839
		}
		b.Send(&tele.User{ID: targetAdmin}, msg, approveBtn)
		return c.Send("Заявка отправлена администратору.")
	})

	b.Handle(&tele.Btn{Unique: "approve"}, func(c tele.Context) error {
		targetID := c.Data()
		newUser := database.User{
			UUID:              uuid.New().String(),
			Username:          fmt.Sprintf("user_%s", targetID),
			TelegramID:        parseInt(targetID),
			Status:            "active",
			TrafficLimit:      30 * 1024 * 1024 * 1024,
			SubscriptionToken: database.GenerateToken(),
		}
		database.DB.Create(&newUser)
		service.GenerateAndReload()
		return c.Edit("✅ Пользователь создан.")
	})

	// --- Логика кнопки "Подключиться" ---
	b.Handle(&btnConnect, func(c tele.Context) error {
		return c.Send("Как вы хотите получить настройки?", connectMenu)
	})

	b.Handle(&tele.Btn{Unique: "conn_link"}, func(c tele.Context) error {
		user, settings := getUserAndSettings(c.Sender().ID)
		// IP адрес сервера
		link := service.GenerateLink(user, settings, "49.13.201.110")
		return c.Send(fmt.Sprintf("`%s`", link), tele.ModeMarkdown)
	})

	b.Handle(&tele.Btn{Unique: "conn_qr"}, func(c tele.Context) error {
		user, settings := getUserAndSettings(c.Sender().ID)
		link := service.GenerateLink(user, settings, "49.13.201.110")

		// Генерируем QR код в память
		qr, err := qrcode.Encode(link, qrcode.Medium, 256)
		if err != nil {
			return c.Send("❌ Ошибка генерации QR кода.")
		}

		// Отправляем как фото
		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(qr)), Caption: "Сканируйте этот код в приложении Hiddify"}
		return c.Send(photo)
	})

	b.Handle(&btnStatus, func(c tele.Context) error {
		user, _ := getUserAndSettings(c.Sender().ID)
		msg := fmt.Sprintf("📊 Трафик: %d / %d", user.TrafficUsed, user.TrafficLimit)
		return c.Send(msg)
	})

	// --- ОБНОВЛЕННАЯ КНОПКА ПОМОЩЬ (Hiddify) ---
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
4. Если нет: Configs -> "+" -> Add Subscription URL.

❓ Если возникли проблемы, пишите администратору.`

		return c.Send(helpMsg, tele.ModeMarkdown)
	})

	b.Start()
}

func getUserAndSettings(tgID int64) (database.User, database.SystemSettings) {
	var user database.User
	database.DB.Where("telegram_id = ?", tgID).First(&user)
	var settings database.SystemSettings
	database.DB.First(&settings)
	return user, settings
}

func parseInt(s string) int64 {
	var i int64
	fmt.Sscanf(s, "%d", &i)
	return i
}
