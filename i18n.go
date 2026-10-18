package main

// UI language. Russian strings are the keys; tr() maps them to English when
// the language is "en". The language comes from Config.Lang, defaulting to the
// Windows UI language (Russian -> ru, anything else -> en).

var uiLang = "ru"

var languages = []struct{ code, name string }{
	{"ru", "Русский"},
	{"en", "English"},
}

func langName(code string) string {
	for _, l := range languages {
		if l.code == code {
			return l.name
		}
	}
	return code
}

var procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")

// systemLang returns "ru" for a Russian Windows UI, "en" otherwise.
func systemLang() string {
	id, _, _ := procGetUserDefaultUILanguage.Call()
	if id&0x3FF == 0x19 { // LANG_RUSSIAN
		return "ru"
	}
	return "en"
}

// tr translates a Russian UI string into the current language.
func tr(s string) string {
	if uiLang == "en" {
		if v, ok := en[s]; ok {
			return v
		}
	}
	return s
}

var en = map[string]string{
	" исключён. Нажмите «Сохранить и применить».":      " excluded. Click “Save and apply”.",
	" уже есть во входах":                              " is already an input",
	". Нажмите «Сохранить и применить».":               ". Click “Save and apply”.",
	": не запущена":                                    ": not running",
	": пока нельзя, процесс ещё не пользовался звуком": ": not yet, the process has not used audio",
	"maxmix — микшер для записи":                       "maxmix — recording mixer",
	"Автозапуск: ":                                     "Autostart: ",
	"В записи":                                         "Recorded",
	"Виртуальное устройство вывода":                    "Virtual output device",
	"Вкл":             "On",
	"Всё, что звучит": "Everything playing",
	"Вход %d: не задано имя программы": "Input %d: no program name",
	"Вход %d: не задано устройство":    "Input %d: no device",
	"Входы":          "Inputs",
	"Выйти":          "Exit",
	"Выкл":           "Off",
	"Выход и запись": "Output & recording",
	"Выход не открыт: устройство не найдено": "Output not open: device not found",
	"Выход":                   "Exit",
	"Громкость":               "Volume",
	"Добавить как вход":       "Add as input",
	"Добавить":                "Add",
	"Добавлен вход ":          "Added input ",
	"Журнал":                  "Log",
	"Закрыть":                 "Close",
	"Запуск вместе с Windows": "Run on startup",
	"Звучит сейчас":           "Playing now",
	"Значение":                "Value",
	"Игра (авто)":             "Game (auto)",
	"Игра не найдена":         "No game detected",
	"Исключить из авто":       "Exclude from auto",
	"Исключить из авто-режима (через запятую)":                                                             "Exclude from auto mode (comma separated)",
	"Микс играет сюда, Steam и NVIDIA пишут с этого устройства":                                            "The mix plays here; Steam and NVIDIA record from this device",
	"Микшер игры и Discord в одно виртуальное устройство для записи Steam / NVIDIA. Go, без зависимостей.": "Mixes the game and Discord into one virtual device for Steam / NVIDIA recordings. Go, no dependencies.",
	"Нажмите «Сохранить и применить»":                                                                      "Click “Save and apply”",
	"Направить на выход": "Route to output",
	"Настройки":          "Settings",
	"Не установлен. Нужен как виртуальное устройство; после установки перезагрузите систему": "Not installed. Needed as the virtual device; reboot after installing",
	"Нет входов типа «Игра (авто)» или «Всё, что звучит»":                                    "No “Game (auto)” or “Everything playing” inputs",
	"О программе": "About",
	"Общие":       "General",
	"Окно на весь экран, на переднем плане или запущено из лаунчера. Сейчас игра не найдена": "Fullscreen window, foreground window or launched from a game launcher. No game detected right now",
	"Определять игру автоматически":             "Detect the game automatically",
	"Остановить микшер и убрать иконку из трея": "Stop the mixer and remove the tray icon",
	"Открыть лог":      "Open log",
	"Открыть":          "Open",
	"Ошибка: ":         "Error: ",
	"Переназначение":   "Routing",
	"Проверка…":        "Checking…",
	"Программа":        "Program",
	"Программы записи": "Recorder apps",
	"Процесс":          "Process",
	"Процессы, чьё устройство по умолчанию подменяется на выход": "Processes whose default device is redirected to the output",
	"Свёрнуто в трей, при входе в систему":                       "Minimised to the tray at sign-in",
	"Сейчас захвачено":      "Capturing now",
	"Сейчас: ":              "Now: ",
	"Снять":                 "Undo",
	"Сохранено и применено": "Saved and applied",
	"Сохранить и применить": "Save and apply",
	"Статус":                "Status",
	"Тип":                   "Type",
	"Удалить":               "Remove",
	"Уровень микса":         "Mix level",
	"Установить":            "Install",
	"Установка VB-CABLE: подтвердите запрос UAC": "Installing VB-CABLE: confirm the UAC prompt",
	"Установлен: устройство CABLE Input найдено": "Installed: CABLE Input device found",
	"Устройство":     "Device",
	"да":             "yes",
	"закрыта":        "closed",
	"играет":         "playing",
	"исключений: %d": "excluded: %d",
	"нет":            "no",
	"тихо":           "silent",
	"Язык":           "Language",
	"Язык интерфейса; окно настроек откроется заново":                    "Interface language; the settings window reopens",
	"Виртуальный кабель для записи; устройство по умолчанию не меняется": "Virtual cable for recording; the default device is left alone",
}

// Self-check strings.
func init() {
	for k, v := range map[string]string{
		"ок":       "ok",
		"внимание": "warning",
		"ошибка":   "error",
		"справка":  "info",

		"Проверка":        "Check",
		"Проверка записи": "Recording check",
		"Запустить":       "Run",
		"Итог":            "Result",
		"Подробности":     "Details",
		"измеряем…":       "measuring…",
		"Идёт проверка: включите звук в игре и скажите что-нибудь в микрофон":  "Check running: make the game play sound and speak into the microphone",
		"Замеряет звук каждого входа, микса и микрофона и пишет образцы в WAV": "Measures every input, the mix and the microphone, and writes WAV samples",

		"Виртуальное устройство":         "Virtual device",
		"Звук в записи":                  "Sound in the recording",
		"не удалось слушать выход: ":     "cannot listen to the output: ",
		"Источники":                      "Inputs",
		"Вход: ":                         "Input: ",
		"Несколько источников":           "Several sources",
		"Микрофон: ":                     "Microphone: ",
		"Микрофон и микшер":              "Microphone and the mixer",
		"Устройство записи по умолчанию": "Default recording device",
		"Устройство вывода по умолчанию": "Default playback device",
		"Записанные образцы":             "Recorded samples",

		"устройство не найдено":                        "device not found",
		"процесс не запущен":                           "process not running",
		"игра не найдена":                              "no game detected",
		"нет процессов со звуком":                      "no process is playing",
		"нечего захватывать":                           "nothing to capture",
		"тишина: запись будет без звука":               "silence: the recording would have no sound",
		"есть звук, пик ":                              "sound present, peak ",
		"захватов:":                                    "captures:",
		"захват работает, но источник молчит":          "capture works, but the source is silent",
		"звук только с":                                "sound only from",
		"молчат:":                                      "silent:",
		"ни один источник не дал звука":                "no source produced any sound",
		"устройство записи по умолчанию не найдено":    "no default recording device found",
		"тишина: скажите что-нибудь во время проверки": "silence: say something while the check runs",
		"микрофон пишут Steam и NVIDIA сами; maxmix его не трогает и не переключает": "Steam and NVIDIA record the microphone themselves; maxmix never touches or switches it",
		"это виртуальный кабель, а не микрофон":                                      "this is a virtual cable, not a microphone",
		"совпадает с выходом микшера, наушники будут молчать":                        "same as the mixer output, the headphones would be silent",
		"не запущена":    "not running",
		"не на выходе: ": "not on the output: ",
	} {
		en[k] = v
	}
}

func init() {
	for k, v := range map[string]string{
		"микрофон выключен (mute): голоса в записи не будет": "microphone is muted: there would be no voice in the recording",
		"громкость микрофона почти на нуле:":                 "microphone volume is near zero:",
		"громкость": "volume",
		"но устройство приглушено в Windows": "but the device is muted in Windows",
	} {
		en[k] = v
	}
}

func init() {
	for k, v := range map[string]string{
		"Steam": "Steam",
		"настройки записи не найдены":  "recording settings not found",
		"Steam: фоновая запись":        "Steam: background recording",
		"выключена в настройках Steam": "disabled in Steam settings",
		"включена":                   "enabled",
		"Steam: что пишется":         "Steam: what it records",
		"только звук игры":           "game audio only",
		"игра и выбранные программы": "game and selected programs",
		"весь системный звук":        "all system audio",
		"надёжнее «весь системный звук»: тогда пишется весь микс с ": "“all system audio” is safer: then the whole mix from ",
		"Steam: выбранные программы":                                 "Steam: selected programs",
		"путь устарел":                              "path is stale",
		"выберите программу в Steam заново":         "select the program in Steam again",
		"список пуст: кроме игры ничего не пишется": "the list is empty: nothing but the game is recorded",
		"Steam: микрофон":                           "Steam: microphone",
		"выключен в настройках записи Steam: голоса в клипе не будет": "disabled in Steam recording settings: the clip would have no voice",
		"включён":               "enabled",
		"Steam: запись на диск": "Steam: writing to disk",
		"последний фрагмент":    "last fragment",
		"возраст":               "age",
		"с":                     "s",
		"идёт прямо сейчас: ":   "running right now: ",
		"файлов записи нет":     "no recording files",
	} {
		en[k] = v
	}
}

func init() {
	for k, v := range map[string]string{
		"Микрофон":                               "Microphone",
		"микрофон не найден":                     "microphone not found",
		"Вход %d: не задан микрофон":             "Input %d: no microphone",
		"Прослушивать микрофон":                  "Monitor the microphone",
		"Прослушивание микрофона":                "Microphone monitoring",
		"Только на время проверки, не постоянно": "During the check only, never continuously",
		"только на время проверки:":              "during the check only:",
		"Выключено":                              "Off",
		"микрофон добавлен во вход микса, поэтому голос попадёт в запись даже с выключенным микрофоном в рекордере": "the microphone is an input of the mix, so the voice lands in the recording even with the recorder's own mic off",
		"включён, а микрофон ещё и в миксе: голос запишется дважды":                                                 "enabled while the microphone is also in the mix: the voice would be recorded twice",
		"выключен, но микрофон идёт через микс":                                                                     "disabled, but the microphone goes through the mix",
	} {
		en[k] = v
	}
}

func init() {
	for k, v := range map[string]string{
		"NVIDIA":           "NVIDIA",
		"NVIDIA: микрофон": "NVIDIA: microphone",
		"выключен в NVIDIA: голоса в клипе не будет":                                                 "disabled in NVIDIA: the clip would have no voice",
		"включён, но устройство не выбрано":                                                          "enabled, but no device is selected",
		"не удалось слушать: ":                                                                       "could not listen: ",
		"этот же микрофон есть во входах микса: голос запишется дважды":                              "the same microphone is also an input of the mix: the voice would be recorded twice",
		"молчит; выключите микрофон в NVIDIA: голос уже идёт через микс":                             "silent; turn the microphone off in NVIDIA: the voice already goes through the mix",
		"молчит, вторая дорожка будет тихой; выключите микрофон в NVIDIA: голос уже идёт через микс": "silent, the second track would be quiet; turn the microphone off in NVIDIA: the voice already goes through the mix",
		"молчит, голоса в клипе не будет; выберите в NVIDIA настоящий микрофон":                      "silent, the clip would have no voice; select a real microphone in NVIDIA",
		"NVIDIA: последний клип":                                                                     "NVIDIA: last clip",
		"клипов NVIDIA нет":                                                                          "no NVIDIA clips",
		"NVIDIA: дорожки звука":                                                                      "NVIDIA: audio tracks",
		"в последнем клипе нет звука":                                                                "the last clip has no audio track",
		"мин": "min",
		"ч":   "h",
		"дн":  "d",
	} {
		en[k] = v
	}
}
