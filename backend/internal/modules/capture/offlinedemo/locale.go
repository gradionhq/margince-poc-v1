// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package offlinedemo

// What language the generated correspondence is written in.
//
// It was German for everybody, and the subject line was built by concatenating
// a German word onto the account's own name — so a Korean government agency
// received "Kickoff 중소기업기술정보진흥원" with a German body and "Viele Grüße"
// at the bottom. 44 of the 235 generated activities in the demo were German
// mail sent to Vietnamese and Korean accounts.
//
// That is not a translation nicety. The demo dataset exists to be shown in
// Hanoi and Seoul, and a Vietnamese manufacturer reading German boilerplate is
// the one thing it cannot survive being.
//
// DERIVED FROM THE DOMAIN, not configured. The seeder has its own localeFor in
// tools/seed-demo/locale.go, reading datasets/v1/company-locale.json — but that
// runs in the seeder binary against a dataset directory, and this runs in the
// WORKER, which has neither. The domain is what both have, and it is already on
// the Account. So the two answer the same question from the same evidence
// without one importing the other.
//
// The disagreement that matters is deliberate. company-locale.json marks Korean
// companies `en`, because that is what their WEBSITES publish — and the site
// reader is what that file serves. Correspondence is a different question: you
// write to a Korean agency in Korean. Hence localeKO here and no localeKO
// there, and the seeder's file is not wrong for lacking it.

import "strings"

// docLocale is the language one account's correspondence is written in.
type docLocale string

const (
	localeDE docLocale = "de"
	localeVI docLocale = "vi"
	localeKO docLocale = "ko"
	localeEN docLocale = "en"
)

// localeOf reads the correspondence language off the domain.
//
// The suffix is the whole rule here, unlike the seeder's, which consults a
// hand-checked file. That is a smaller claim and the right one for a fallback:
// this only decides the language of GENERATED filler, and a company whose
// domain says nothing gets German, which is the dataset's own majority.
//
// A `.com` Korean or Vietnamese company therefore reads as German here and is
// correct in the seeder. That gap is closed by scripting the account's threads
// rather than by teaching a suffix table to guess — a guess that is wrong for a
// fifth of the Automation World list, which is exactly why the seeder stopped
// guessing and read a file instead.
func localeOf(domain string) docLocale {
	domain = strings.ToLower(strings.TrimSpace(domain))
	switch {
	case strings.HasSuffix(domain, ".vn"), strings.HasSuffix(domain, ".com.vn"):
		return localeVI
	case strings.HasSuffix(domain, ".kr"), strings.HasSuffix(domain, ".co.kr"),
		strings.HasSuffix(domain, ".or.kr"):
		return localeKO
	default:
		return localeDE
	}
}

// templateWords is every phrase the generator wraps a message body in.
//
// A struct rather than a map keyed by string, for the same reason
// seed-demo/locale.go's contractWords is one: a missing phrase is then a
// compile error instead of an empty line in a mail nobody proofreads.
type templateWords struct {
	// Hello is the salutation, taking the name it addresses. A function
	// because the name's POSITION differs: German and English lead with the
	// greeting, Korean appends an honorific to the name itself.
	Hello func(name string) string
	// SignOff closes the message.
	SignOff string
	// Kickoff, Invoice, Offboarding, Intro, Enquiry and Cold are the subject
	// lines, and Meeting prefixes a calendar entry.
	Kickoff     string
	Invoice     string
	Offer       string
	Offboarding string
	Intro       string
	Enquiry     string
	Cold        string
	Meeting     string
	// MeetingBody is the one-line body a calendar entry carries.
	MeetingBody string
}

// Greeting is the salutation for one addressee.
func (w templateWords) Greeting(name string) string { return w.Hello(name) }

var vocabulary = map[docLocale]templateWords{
	localeDE: {
		Hello:       func(n string) string { return "Hallo " + n + "," },
		SignOff:     "Viele Grüße",
		Kickoff:     "Kickoff",
		Invoice:     "Rechnung",
		Offer:       "Angebot",
		Offboarding: "Kündigung bestätigt",
		Intro:       "Kurzer Austausch?",
		Enquiry:     "Anfrage über die Website",
		Cold:        "Kurze Frage zu Ihrem Shop",
		Meeting:     "Termin",
		MeetingBody: "Abstimmung, 45 Minuten, per Video.",
	},
	localeVI: {
		Hello:       func(n string) string { return "Kính gửi " + n + "," },
		SignOff:     "Trân trọng",
		Kickoff:     "Họp khởi động",
		Invoice:     "Hóa đơn",
		Offer:       "Báo giá",
		Offboarding: "Xác nhận chấm dứt hợp đồng",
		Intro:       "Xin phép được trao đổi ngắn",
		Enquiry:     "Liên hệ qua website",
		Cold:        "Một câu hỏi ngắn về hệ thống của quý công ty",
		Meeting:     "Lịch họp",
		MeetingBody: "Trao đổi 45 phút, họp trực tuyến.",
	},
	localeKO: {
		// Korean appends the honorific to the name rather than leading with a
		// greeting word: "박준서 님께" is how the salutation is actually written.
		Hello:       func(n string) string { return n + " 님께," },
		SignOff:     "감사합니다",
		Kickoff:     "착수 회의",
		Invoice:     "청구서",
		Offer:       "제안서",
		Offboarding: "계약 종료 확인",
		Intro:       "짧게 논의 가능하실까요",
		Enquiry:     "홈페이지를 통한 문의",
		Cold:        "귀사 시스템에 관한 짧은 문의",
		Meeting:     "일정",
		MeetingBody: "45분 화상 회의.",
	},
	localeEN: {
		Hello:       func(n string) string { return "Hi " + n + "," },
		SignOff:     "Best regards",
		Kickoff:     "Kickoff",
		Invoice:     "Invoice",
		Offer:       "Offer",
		Offboarding: "Cancellation confirmed",
		Intro:       "A short conversation?",
		Enquiry:     "Enquiry through the website",
		Cold:        "A quick question about your platform",
		Meeting:     "Meeting",
		MeetingBody: "A 45-minute call, by video.",
	},
}

// wordsFor is the vocabulary for a language, falling back to German rather
// than to a zero struct — a message with a nil Hello would panic, and one with
// blank phrases would read as a rendering bug rather than a missing language.
func wordsFor(locale docLocale) templateWords {
	if words, ok := vocabulary[locale]; ok {
		return words
	}
	return vocabulary[localeDE]
}

// The thread keys, named once. They appear in threadsFor, in every language's
// body map, and in the message ids — goconst is right that a typo in one of
// those places would be a silent miss rather than a compile error.
//
// threadInbound is deliberately the same string as directionInbound: the
// prospect thread is keyed by the direction it opens in, which is how it was
// written before the languages were split out.
const (
	threadKickoff     = "kickoff"
	threadInvoice     = "invoice"
	threadOffboarding = "offboarding"
	threadOffer       = "offer"
	threadIntro       = "intro"
	threadInbound     = directionInbound
	threadCold        = "cold"
)

// threadBodies is what each thread SAYS, per language.
//
// Keyed by the threadSpec's own Key, so a thread added to threadsFor without a
// translation is caught by bodiesFor's fallback rather than emitting an empty
// message. The three entries are opener, reply, second reply — the same shape
// threadSpec.Body has always had.
var threadBodies = map[docLocale]map[string][3]string{
	localeDE: {
		threadKickoff: {
			"vielen Dank für Ihr Vertrauen. Anbei der Terminvorschlag für den Kickoff.",
			"passt uns gut, wir bringen die Fachbereiche mit.",
			"prima, Einladung ist raus. Agenda hängt an.",
		},
		threadInvoice: {
			"kurze Rückfrage zur letzten Rechnung — ist die Position 3 anteilig berechnet?",
			"ja, anteilig bis zum Periodenende. Ich schicke die Aufstellung mit.", "",
		},
		threadOffboarding: {
			"wir bestätigen den Eingang Ihrer Kündigung zum Ende der Laufzeit.",
			"danke für die Bestätigung und die Zusammenarbeit.", "",
		},
		threadOffer: {
			"anbei unser Angebot wie besprochen. Die Staffel greift ab 50 Lizenzen.",
			"danke — zwei Rückfragen zur Laufzeit und zum Support-Level.",
			"beides gerne im Termin, Vorschlag hängt an.",
		},
		threadIntro: {
			"wir arbeiten mit mehreren Häusern Ihrer Größe — lohnt ein kurzer Austausch?",
			"gerne, schicken Sie ein paar Slots.", "",
		},
		threadInbound: {
			"wir prüfen gerade Anbieter und würden gerne mehr erfahren.",
			"sehr gerne — ich melde mich mit zwei Terminvorschlägen.", "",
		},
		threadCold: {"eine kurze Frage zu Ihrer Plattform — haben Sie zehn Minuten?", "", ""},
	},
	localeVI: {
		threadKickoff: {
			"cảm ơn quý công ty đã tin tưởng. Chúng tôi xin gửi đề xuất lịch họp khởi động.",
			"lịch này phù hợp với chúng tôi, chúng tôi sẽ mời thêm bộ phận chuyên môn.",
			"vâng, thư mời đã gửi. Chương trình họp đính kèm.",
		},
		threadInvoice: {
			"chúng tôi có một câu hỏi ngắn về hóa đơn vừa rồi — mục 3 được tính theo tỷ lệ phải không?",
			"vâng, tính theo tỷ lệ đến hết kỳ. Tôi xin gửi kèm bảng chi tiết.", "",
		},
		threadOffboarding: {
			"chúng tôi xác nhận đã nhận được thông báo chấm dứt hợp đồng khi hết hạn.",
			"cảm ơn xác nhận và cảm ơn thời gian hợp tác vừa qua.", "",
		},
		threadOffer: {
			"xin gửi báo giá như đã trao đổi. Mức chiết khấu áp dụng từ 50 giấy phép.",
			"cảm ơn — chúng tôi có hai câu hỏi về thời hạn và mức hỗ trợ.",
			"cả hai xin được trao đổi trong buổi họp, đề xuất đính kèm.",
		},
		threadIntro: {
			"chúng tôi đang làm việc với một số doanh nghiệp cùng quy mô — anh/chị có muốn trao đổi ngắn không?",
			"vâng, anh gửi giúp vài khung giờ.", "",
		},
		threadInbound: {
			"chúng tôi đang tìm hiểu các nhà cung cấp và muốn biết thêm thông tin.",
			"rất sẵn lòng — tôi sẽ gửi hai đề xuất lịch họp.", "",
		},
		threadCold: {"một câu hỏi ngắn về hệ thống của quý công ty — anh/chị có mười phút không?", "", ""},
	},
	localeKO: {
		threadKickoff: {
			"믿고 맡겨 주셔서 감사합니다. 착수 회의 일정을 제안드립니다.",
			"제안하신 일정 좋습니다. 실무 부서도 함께 참석하겠습니다.",
			"네, 초대장 발송했습니다. 안건은 첨부드립니다.",
		},
		threadInvoice: {
			"지난 청구서 관련해 간단히 여쭙습니다 — 3번 항목은 일할 계산된 것인가요?",
			"네, 해당 기간 종료일까지 일할 계산했습니다. 내역서 함께 보내드립니다.", "",
		},
		threadOffboarding: {
			"계약 만료 시점의 종료 요청을 접수했음을 확인드립니다.",
			"확인 감사드리며, 그동안 함께해 주셔서 감사했습니다.", "",
		},
		threadOffer: {
			"논의드린 대로 제안서를 보내드립니다. 50 라이선스부터 할인이 적용됩니다.",
			"감사합니다 — 계약 기간과 지원 범위에 대해 두 가지 문의가 있습니다.",
			"두 가지 모두 회의에서 설명드리겠습니다. 제안 사항 첨부합니다.",
		},
		threadIntro: {
			"비슷한 규모의 기업들과 협업하고 있습니다 — 짧게 논의해 보실 수 있을까요?",
			"네 좋습니다. 가능한 시간대를 몇 개 보내주세요.", "",
		},
		threadInbound: {
			"현재 공급업체를 검토 중이며 좀 더 자세한 내용을 알고 싶습니다.",
			"네, 회의 일정 두 가지를 제안드리겠습니다.", "",
		},
		threadCold: {"귀사 플랫폼에 관해 짧게 여쭙고 싶습니다 — 10분 정도 가능하실까요?", "", ""},
	},
}

// bodiesFor is one thread's text in one language.
//
// Falls back to German, and then to the German kickoff, so a thread key added
// to threadsFor without a translation still emits something a reader can
// recognise as unfinished rather than an empty body.
func bodiesFor(locale docLocale, key string) [3]string {
	if byKey, ok := threadBodies[locale]; ok {
		if body, ok := byKey[key]; ok {
			return body
		}
	}
	if body, ok := threadBodies[localeDE][key]; ok {
		return body
	}
	return threadBodies[localeDE][threadKickoff]
}
