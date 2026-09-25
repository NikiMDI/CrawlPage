package site

type link struct {
	label            string
	href             string
	absoluteInternal bool
}

type page struct {
	title string
	text  string
	links []link
}

func createPages() map[string]page {
	return map[string]page{
		"/index.html": {
			title: "Стартовая страница графа",
			text:  "Отсюда обходчик попадает в цикл, общую вершину, глубокую ветку и на специальные тестовые маршруты.",
			links: []link{
				{label: "Войти в цикл A → B → C → A", href: "a.html"},
				{label: "Начать глубокую ветку", href: "depth-1.html"},
				{label: "Общая вершина по абсолютному URL", href: "/hub.html", absoluteInternal: true},
				{label: "Один redirect", href: "/redirect-once"},
				{label: "Цепочка из двух redirect", href: "/redirect-chain/start"},
				{label: "Медленная страница", href: "/slow.html"},
				{label: "Несуществующая страница", href: "/missing-page.html"},
				{label: "Намеренная ошибка 500", href: "/server-error"},
				{label: "Внешний сайт example.com", href: "https://example.com/"},
			},
		},
		"/a.html": {
			title: "Вершина A",
			text:  "Первая вершина явного цикла. Она ведёт в B и в общую вершину, создавая несколько исходящих рёбер.",
			links: []link{
				{label: "Перейти в B", href: "b.html"},
				{label: "Перейти в общую вершину", href: "/hub.html"},
				{label: "Проверить медленный ответ", href: "/slow.html"},
			},
		},
		"/b.html": {
			title: "Вершина B",
			text:  "Вторая вершина цикла с дополнительным ребром в тот же URL общей вершины, что и со страницы A.",
			links: []link{
				{label: "Перейти в C", href: "c.html"},
				{label: "Перейти в общую вершину", href: "/hub.html"},
				{label: "Перейти через одиночный redirect", href: "/redirect-once"},
			},
		},
		"/c.html": {
			title: "Вершина C",
			text:  "Третья вершина замыкает цикл обратной ссылкой на A и позволяет вернуться к началу обхода.",
			links: []link{
				{label: "Замкнуть цикл и вернуться в A", href: "a.html"},
				{label: "Вернуться на старт", href: "index.html"},
			},
		},
		"/hub.html": {
			title: "Общая вершина",
			text:  "На этот URL ссылаются разные страницы. Такое слияние рёбер является характерной частью графа, а не дерева.",
			links: []link{
				{label: "Перейти в C", href: "c.html"},
				{label: "Вернуться на старт", href: "/index.html"},
			},
		},
		"/depth-1.html": {
			title: "Глубина 1",
			text:  "Первый шаг отдельной цепочки. При старте с index.html эта страница находится на глубине один.",
			links: []link{
				{label: "Продолжить на глубину 2", href: "depth-2.html"},
			},
		},
		"/depth-2.html": {
			title: "Глубина 2",
			text:  "Второй шаг цепочки ведёт дальше единственным ребром, поэтому короткого пути к конечной странице нет.",
			links: []link{
				{label: "Продолжить на глубину 3", href: "depth-3.html"},
			},
		},
		"/depth-3.html": {
			title: "Глубина 3",
			text:  "Ссылка ниже указывает за предел глубины три: её цель будет обнаружена на глубине четыре.",
			links: []link{
				{label: "Открыть страницу глубже лимита", href: "deep-target.html"},
			},
		},
		"/deep-target.html": {
			title: "Глубокая целевая страница",
			text:  "Эта вершина достигается на глубине четыре и проверяет, что обходчик соблюдает установленный лимит три.",
			links: []link{
				{label: "Вернуться к началу графа", href: "index.html"},
			},
		},
		"/slow.html": {
			title: "Медленная страница",
			text:  "Сервер специально задерживает этот ответ, чтобы клиент с коротким timeout успел завершить запрос ошибкой.",
			links: []link{
				{label: "После ожидания перейти в общую вершину", href: "/hub.html"},
			},
		},
	}
}
