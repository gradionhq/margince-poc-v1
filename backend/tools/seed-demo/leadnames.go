// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Who a generated lead is, by name.
//
// Separate from generated.go because it is data rather than logic, and because
// the pools have to be big: a lead list where the same person appears nine
// times reads as machine-made, and that is exactly what an 8x8 pool produced.

import "strings"

// nameLocale is which naming culture a generated lead is drawn from. It is
// deliberately NOT docLocale: a Korean company's paper is written in English
// (the Korean sites publish English, so their contracts do too), but a lead
// sitting at that company is still called Park, not Achterberg. One is the
// language of a document, the other is what people are named.
type nameLocale string

const (
	namesDE nameLocale = "de"
	namesVI nameLocale = "vi"
	namesKO nameLocale = "ko"
	namesEN nameLocale = "en"
)

// nameLocaleFor answers which pool a company's people are named from.
//
// The Korean domains are listed explicitly because .kr and .co.kr are not the
// whole set -- an .or.kr is a public body and DACELL sits on a .com -- and
// because company-locale.json answers a different question. That file says
// which language the DOCUMENTS are in, and it maps every Korean company to
// English, which is right for a contract and wrong for a person's name.
func nameLocaleFor(domain string) nameLocale {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if koreanNameDomains[domain] {
		return namesKO
	}
	switch {
	case strings.HasSuffix(domain, ".kr"):
		return namesKO
	case strings.HasSuffix(domain, ".vn"):
		return namesVI
	}
	if localeFor(domain) == localeVI {
		return namesVI
	}
	if localeFor(domain) == localeEN {
		return namesEN
	}
	return namesDE
}

// koreanNameDomains are the Korean companies whose domain does not end in .kr.
var koreanNameDomains = map[string]bool{
	"dacell.com":     true,
	"hongikinfo.com": true,
	"nidsoft.com":    true,
}

// The pools are large enough that 45 generated leads do not collide into the
// same handful of people. The previous 8x8 German pool produced "Kilian
// Wenzel" nine times across the dataset and put a German name on every Korean
// and Vietnamese company, both of which are visible on the first page of the
// lead list.
var leadNamesByLocale = map[nameLocale]struct{ First, Last []string }{
	namesDE: {
		First: []string{"Jonas", "Mareike", "Tobias", "Svenja", "Kilian", "Annika", "Fabian", "Carla",
			"Lennart", "Johanna", "Sebastian", "Franziska", "Matthias", "Katrin", "Philipp", "Nadine",
			"Christoph", "Verena", "Dominik", "Sabine"},
		Last: []string{"Wenzel", "Achterberg", "Roth", "Lindqvist", "Sommer", "Baumgart", "Reinhold", "Kessler",
			"Hoffmann", "Brandt", "Krüger", "Seibert", "Vogler", "Naumann", "Ziegler", "Dreher",
			"Lehmann", "Osterloh", "Hartwig", "Bergmann"},
	},
	namesVI: {
		First: []string{"Minh", "Lan", "Hùng", "Thảo", "Dũng", "Trang", "Khánh", "Ngọc",
			"Tuấn", "Hà", "Quang", "Linh", "Sơn", "Mai", "Đức", "Yến",
			"Nam", "Phương", "Thành", "Vân"},
		Last: []string{"Nguyễn", "Trần", "Lê", "Phạm", "Hoàng", "Vũ", "Đặng", "Bùi",
			"Đỗ", "Hồ", "Ngô", "Dương", "Lý", "Phan", "Võ", "Đinh"},
	},
	namesKO: {
		First: []string{"Ji-woo", "Min-jun", "Seo-yeon", "Do-yun", "Ha-eun", "Jun-seo", "Soo-ah", "Tae-hyun",
			"Eun-ji", "Sang-hoon", "Yu-jin", "Hyun-woo", "Da-eun", "Jae-min", "So-yeong", "Dong-hyun"},
		Last: []string{"Kim", "Lee", "Park", "Choi", "Jung", "Kang", "Cho", "Yoon",
			"Jang", "Lim", "Han", "Oh", "Seo", "Shin", "Kwon", "Hwang"},
	},
	namesEN: {
		First: []string{"Emma", "Oliver", "Sophie", "Daniel", "Laura", "Thomas", "Claire", "Adam",
			"Rachel", "Simon", "Hannah", "Peter", "Alice", "Mark", "Julia", "Steven",
			"Nina", "Paul", "Ellen", "George"},
		Last: []string{"Whitfield", "Barrow", "Kingsley", "Harper", "Ellison", "Radcliffe", "Marsden", "Prescott",
			"Ashworth", "Bramley", "Corrigan", "Danvers", "Everly", "Fairbanks", "Grayson", "Holbrook"},
	},
}

var leadTitlesByLocale = map[nameLocale][]string{
	namesDE: {"Head of E-Commerce", "Digital Product Owner", "Leiter IT", "Marketing Manager",
		"Head of Operations", "Projektleiterin Digitalisierung", "Leiter Vertrieb", "Bereichsleiter IT"},
	namesVI: {"Giám đốc Kỹ thuật", "Trưởng phòng Dự án", "Giám đốc Điều hành", "Trưởng phòng Kinh doanh",
		"Phụ trách CNTT", "Quản lý Sản xuất"},
	namesKO: {"IT팀장", "사업개발팀장", "기술연구소장", "구매팀장", "영업본부장", "생산관리팀장"},
	namesEN: {"Head of E-Commerce", "Digital Product Owner", "Head of IT", "Marketing Manager",
		"Head of Operations", "Director of Digital", "VP Sales", "Head of Procurement"},
}
