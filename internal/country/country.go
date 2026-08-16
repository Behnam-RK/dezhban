// Package country turns an ISO 3166-1 alpha-2 code into something a person can
// read. It is display-only: nothing here validates, and no caller may treat a
// missing name as a bad code.
//
// The table is generated from CLDR's English short names (the same data macOS
// and ICU use), with "CN" shortened from CLDR's "China mainland". It is
// deliberately a plain map rather than a dependency: golang.org/x/text/language
// /display would answer the same question, but it is currently an indirect
// module that no file imports, and promoting it drags CLDR's full data tables
// into a kill-switch daemon that is otherwise stdlib-only. ~10KB of map beats
// megabytes of locale data for a lookup that is always English.
//
// Codes that are not in the table — CLDR gains and loses a few, and
// blockedCountries has never validated membership — degrade to the bare code
// rather than to an empty string or a lie.
package country

import "strings"

// Name returns the English short name for an alpha-2 code, or "" if unknown.
func Name(code string) string {
	return names[strings.ToUpper(strings.TrimSpace(code))]
}

// Label renders a code for display: "Iran (IR)", or just the bare, upper-cased
// code when there is no name for it. Every user-facing site goes through this,
// so an unrecognised code can never render as an empty parenthetical.
func Label(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		return ""
	}
	if n := names[c]; n != "" {
		return n + " (" + c + ")"
	}
	return c
}

// Names maps Name over a list, preserving length so the result pairs with the
// input index-for-index. An unrecognised code yields "" at its index — the same
// degradation Name gives for one code, so a consumer applies one rule ("empty
// name means show the bare code") whether it is holding a single country or a
// list. Use this for structured output; use Labels for text meant to be read.
func Names(codes []string) []string {
	if len(codes) == 0 {
		return nil
	}
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, Name(c))
	}
	return out
}

// Labels maps Label over a list, for the several places that print a
// comma-joined blocklist.
func Labels(codes []string) []string {
	if len(codes) == 0 {
		return nil
	}
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, Label(c))
	}
	return out
}

var names = map[string]string{
	"AC": "Ascension Island",
	"AD": "Andorra",
	"AE": "United Arab Emirates",
	"AF": "Afghanistan",
	"AG": "Antigua & Barbuda",
	"AI": "Anguilla",
	"AL": "Albania",
	"AM": "Armenia",
	"AO": "Angola",
	"AQ": "Antarctica",
	"AR": "Argentina",
	"AS": "American Samoa",
	"AT": "Austria",
	"AU": "Australia",
	"AW": "Aruba",
	"AX": "Åland Islands",
	"AZ": "Azerbaijan",
	"BA": "Bosnia & Herzegovina",
	"BB": "Barbados",
	"BD": "Bangladesh",
	"BE": "Belgium",
	"BF": "Burkina Faso",
	"BG": "Bulgaria",
	"BH": "Bahrain",
	"BI": "Burundi",
	"BJ": "Benin",
	"BL": "St. Barthélemy",
	"BM": "Bermuda",
	"BN": "Brunei",
	"BO": "Bolivia",
	"BQ": "Caribbean Netherlands",
	"BR": "Brazil",
	"BS": "Bahamas",
	"BT": "Bhutan",
	"BV": "Bouvet Island",
	"BW": "Botswana",
	"BY": "Belarus",
	"BZ": "Belize",
	"CA": "Canada",
	"CC": "Cocos (Keeling) Islands",
	"CD": "Congo - Kinshasa",
	"CF": "Central African Republic",
	"CG": "Congo - Brazzaville",
	"CH": "Switzerland",
	"CI": "Côte d’Ivoire",
	"CK": "Cook Islands",
	"CL": "Chile",
	"CM": "Cameroon",
	"CN": "China",
	"CO": "Colombia",
	"CP": "Clipperton Island",
	"CQ": "Sark",
	"CR": "Costa Rica",
	"CU": "Cuba",
	"CV": "Cape Verde",
	"CW": "Curaçao",
	"CX": "Christmas Island",
	"CY": "Cyprus",
	"CZ": "Czechia",
	"DE": "Germany",
	"DG": "Diego Garcia",
	"DJ": "Djibouti",
	"DK": "Denmark",
	"DM": "Dominica",
	"DO": "Dominican Republic",
	"DZ": "Algeria",
	"EA": "Ceuta & Melilla",
	"EC": "Ecuador",
	"EE": "Estonia",
	"EG": "Egypt",
	"EH": "Western Sahara",
	"ER": "Eritrea",
	"ES": "Spain",
	"ET": "Ethiopia",
	"EU": "European Union",
	"EZ": "Eurozone",
	"FI": "Finland",
	"FJ": "Fiji",
	"FK": "Falkland Islands",
	"FM": "Micronesia",
	"FO": "Faroe Islands",
	"FR": "France",
	"GA": "Gabon",
	"GB": "United Kingdom",
	"GD": "Grenada",
	"GE": "Georgia",
	"GF": "French Guiana",
	"GG": "Guernsey",
	"GH": "Ghana",
	"GI": "Gibraltar",
	"GL": "Greenland",
	"GM": "Gambia",
	"GN": "Guinea",
	"GP": "Guadeloupe",
	"GQ": "Equatorial Guinea",
	"GR": "Greece",
	"GS": "So. Georgia & So. Sandwich Isl.",
	"GT": "Guatemala",
	"GU": "Guam",
	"GW": "Guinea-Bissau",
	"GY": "Guyana",
	"HK": "Hong Kong",
	"HM": "Heard & McDonald Islands",
	"HN": "Honduras",
	"HR": "Croatia",
	"HT": "Haiti",
	"HU": "Hungary",
	"IC": "Canary Islands",
	"ID": "Indonesia",
	"IE": "Ireland",
	"IL": "Israel",
	"IM": "Isle of Man",
	"IN": "India",
	"IO": "Chagos Archipelago",
	"IQ": "Iraq",
	"IR": "Iran",
	"IS": "Iceland",
	"IT": "Italy",
	"JE": "Jersey",
	"JM": "Jamaica",
	"JO": "Jordan",
	"JP": "Japan",
	"KE": "Kenya",
	"KG": "Kyrgyzstan",
	"KH": "Cambodia",
	"KI": "Kiribati",
	"KM": "Comoros",
	"KN": "St. Kitts & Nevis",
	"KP": "North Korea",
	"KR": "South Korea",
	"KW": "Kuwait",
	"KY": "Cayman Islands",
	"KZ": "Kazakhstan",
	"LA": "Laos",
	"LB": "Lebanon",
	"LC": "St. Lucia",
	"LI": "Liechtenstein",
	"LK": "Sri Lanka",
	"LR": "Liberia",
	"LS": "Lesotho",
	"LT": "Lithuania",
	"LU": "Luxembourg",
	"LV": "Latvia",
	"LY": "Libya",
	"MA": "Morocco",
	"MC": "Monaco",
	"MD": "Moldova",
	"ME": "Montenegro",
	"MF": "St. Martin",
	"MG": "Madagascar",
	"MH": "Marshall Islands",
	"MK": "North Macedonia",
	"ML": "Mali",
	"MM": "Myanmar (Burma)",
	"MN": "Mongolia",
	"MO": "Macao",
	"MP": "Northern Mariana Islands",
	"MQ": "Martinique",
	"MR": "Mauritania",
	"MS": "Montserrat",
	"MT": "Malta",
	"MU": "Mauritius",
	"MV": "Maldives",
	"MW": "Malawi",
	"MX": "Mexico",
	"MY": "Malaysia",
	"MZ": "Mozambique",
	"NA": "Namibia",
	"NC": "New Caledonia",
	"NE": "Niger",
	"NF": "Norfolk Island",
	"NG": "Nigeria",
	"NI": "Nicaragua",
	"NL": "Netherlands",
	"NO": "Norway",
	"NP": "Nepal",
	"NR": "Nauru",
	"NU": "Niue",
	"NZ": "New Zealand",
	"OM": "Oman",
	"PA": "Panama",
	"PE": "Peru",
	"PF": "French Polynesia",
	"PG": "Papua New Guinea",
	"PH": "Philippines",
	"PK": "Pakistan",
	"PL": "Poland",
	"PM": "St. Pierre & Miquelon",
	"PN": "Pitcairn Islands",
	"PR": "Puerto Rico",
	"PS": "Palestinian Territories",
	"PT": "Portugal",
	"PW": "Palau",
	"PY": "Paraguay",
	"QA": "Qatar",
	"QO": "Outlying Oceania",
	"RE": "Réunion",
	"RO": "Romania",
	"RS": "Serbia",
	"RU": "Russia",
	"RW": "Rwanda",
	"SA": "Saudi Arabia",
	"SB": "Solomon Islands",
	"SC": "Seychelles",
	"SD": "Sudan",
	"SE": "Sweden",
	"SG": "Singapore",
	"SH": "St. Helena",
	"SI": "Slovenia",
	"SJ": "Svalbard & Jan Mayen",
	"SK": "Slovakia",
	"SL": "Sierra Leone",
	"SM": "San Marino",
	"SN": "Senegal",
	"SO": "Somalia",
	"SR": "Suriname",
	"SS": "South Sudan",
	"ST": "São Tomé & Príncipe",
	"SV": "El Salvador",
	"SX": "Sint Maarten",
	"SY": "Syria",
	"SZ": "Eswatini",
	"TA": "Tristan da Cunha",
	"TC": "Turks & Caicos Islands",
	"TD": "Chad",
	"TF": "French Southern Territories",
	"TG": "Togo",
	"TH": "Thailand",
	"TJ": "Tajikistan",
	"TK": "Tokelau",
	"TL": "Timor-Leste",
	"TM": "Turkmenistan",
	"TN": "Tunisia",
	"TO": "Tonga",
	"TR": "Türkiye",
	"TT": "Trinidad & Tobago",
	"TV": "Tuvalu",
	"TW": "Taiwan",
	"TZ": "Tanzania",
	"UA": "Ukraine",
	"UG": "Uganda",
	"UM": "U.S. Outlying Islands",
	"UN": "United Nations",
	"US": "United States",
	"UY": "Uruguay",
	"UZ": "Uzbekistan",
	"VA": "Vatican City",
	"VC": "St. Vincent & Grenadines",
	"VE": "Venezuela",
	"VG": "British Virgin Islands",
	"VI": "U.S. Virgin Islands",
	"VN": "Vietnam",
	"VU": "Vanuatu",
	"WF": "Wallis & Futuna",
	"WS": "Samoa",
	"XK": "Kosovo",
	"YE": "Yemen",
	"YT": "Mayotte",
	"ZA": "South Africa",
	"ZM": "Zambia",
	"ZW": "Zimbabwe",
}
