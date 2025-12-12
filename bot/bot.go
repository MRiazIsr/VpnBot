package bot

import (
	"fmt"
	"log"
	"time"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/google/uuid"
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

		// Если не нашли по Telegram ID, пробуем найти по Username (если ты MRiaz)
		// Это поможет привязать твоего существующего юзера к твоему телеграму
		if result.Error != nil {
			var existingUser database.User
			// ВАЖНО: Тут предполагается, что username в ТГ совпадает с именем в базе, или можно привязать через админку
			// Для первого запуска просто проверим, если sender - админ, и есть юзер MRiaz без tg_id, привяжем его
			// Добавил твой ID 124343839 жестко, чтобы сработало даже без настройки ENV
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

		// Отправляем админу (используем переданный ID или твой хардкод)
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
		// IP адрес сервера из твоего конфига
		link := service.GenerateLink(user, settings, "49.13.201.110")
		return c.Send(fmt.Sprintf("`%s`", link), tele.ModeMarkdown)
	})

	b.Handle(&tele.Btn{Unique: "conn_file"}, func(c tele.Context) error {
		// Тут можно сгенерировать .json файл для импорта
		return c.Send("Генерация файла пока в разработке (для Sing-box используйте ссылку).")
	})

	b.Handle(&tele.Btn{Unique: "conn_qr"}, func(c tele.Context) error {
		// Для QR кода нужна библиотека go-qrcode, пока заглушка
		return c.Send("Для QR кода используйте мобильное приложение и ссылку.")
	})

	b.Handle(&btnStatus, func(c tele.Context) error {
		user, _ := getUserAndSettings(c.Sender().ID)
		msg := fmt.Sprintf("📊 Трафик: %d / %d", user.TrafficUsed, user.TrafficLimit)
		return c.Send(msg)
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
