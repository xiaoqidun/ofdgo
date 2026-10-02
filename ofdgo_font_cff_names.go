// Copyright 2025-2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ofdgo

import "strings"

// cffExpertCharset 按CFF规范附录C保存Expert字符集的SID顺序
var cffExpertCharset = [...]uint16{
	0, 1, 229, 230, 231, 232, 233, 234, 235, 236, 237, 238, 13, 14, 15, 99,
	239, 240, 241, 242, 243, 244, 245, 246, 247, 248, 27, 28, 249, 250, 251, 252,
	253, 254, 255, 256, 257, 258, 259, 260, 261, 262, 263, 264, 265, 266, 109, 110,
	267, 268, 269, 270, 271, 272, 273, 274, 275, 276, 277, 278, 279, 280, 281, 282,
	283, 284, 285, 286, 287, 288, 289, 290, 291, 292, 293, 294, 295, 296, 297, 298,
	299, 300, 301, 302, 303, 304, 305, 306, 307, 308, 309, 310, 311, 312, 313, 314,
	315, 316, 317, 318, 158, 155, 163, 319, 320, 321, 322, 323, 324, 325, 326, 150,
	164, 169, 327, 328, 329, 330, 331, 332, 333, 334, 335, 336, 337, 338, 339, 340,
	341, 342, 343, 344, 345, 346, 347, 348, 349, 350, 351, 352, 353, 354, 355, 356,
	357, 358, 359, 360, 361, 362, 363, 364, 365, 366, 367, 368, 369, 370, 371, 372,
	373, 374, 375, 376, 377, 378,
}

// cffExpertSubsetCharset 按CFF规范附录C保存ExpertSubset字符集的SID顺序
var cffExpertSubsetCharset = [...]uint16{
	0, 1, 231, 232, 235, 236, 237, 238, 13, 14, 15, 99, 239, 240, 241, 242,
	243, 244, 245, 246, 247, 248, 27, 28, 249, 250, 251, 253, 254, 255, 256, 257,
	258, 259, 260, 261, 262, 263, 264, 265, 266, 109, 110, 267, 268, 269, 270, 272,
	300, 301, 302, 305, 314, 315, 158, 155, 163, 320, 321, 322, 323, 324, 325, 326,
	150, 164, 169, 327, 328, 329, 330, 331, 332, 333, 334, 335, 336, 337, 338, 339,
	340, 341, 342, 343, 344, 345, 346,
}

// cffStandardStrings 按CFF标准SID顺序保存字形名称
var cffStandardStrings = strings.Fields(`
.notdef space exclam quotedbl numbersign dollar percent ampersand quoteright parenleft parenright asterisk
plus comma hyphen period slash zero one two three four five six
seven eight nine colon semicolon less equal greater question at A B
C D E F G H I J K L M N
O P Q R S T U V W X Y Z
bracketleft backslash bracketright asciicircum underscore quoteleft a b c d e f
g h i j k l m n o p q r
s t u v w x y z braceleft bar braceright asciitilde
exclamdown cent sterling fraction yen florin section currency quotesingle quotedblleft guillemotleft guilsinglleft
guilsinglright fi fl endash dagger daggerdbl periodcentered paragraph bullet quotesinglbase quotedblbase quotedblright
guillemotright ellipsis perthousand questiondown grave acute circumflex tilde macron breve dotaccent dieresis
ring cedilla hungarumlaut ogonek caron emdash AE ordfeminine Lslash Oslash OE ordmasculine
ae dotlessi lslash oslash oe germandbls onesuperior logicalnot mu trademark Eth onehalf
plusminus Thorn onequarter divide brokenbar degree thorn threequarters twosuperior registered minus eth
multiply threesuperior copyright Aacute Acircumflex Adieresis Agrave Aring Atilde Ccedilla Eacute Ecircumflex
Edieresis Egrave Iacute Icircumflex Idieresis Igrave Ntilde Oacute Ocircumflex Odieresis Ograve Otilde
Scaron Uacute Ucircumflex Udieresis Ugrave Yacute Ydieresis Zcaron aacute acircumflex adieresis agrave
aring atilde ccedilla eacute ecircumflex edieresis egrave iacute icircumflex idieresis igrave ntilde
oacute ocircumflex odieresis ograve otilde scaron uacute ucircumflex udieresis ugrave yacute ydieresis
zcaron exclamsmall Hungarumlautsmall dollaroldstyle dollarsuperior ampersandsmall Acutesmall parenleftsuperior parenrightsuperior twodotenleader onedotenleader zerooldstyle
oneoldstyle twooldstyle threeoldstyle fouroldstyle fiveoldstyle sixoldstyle sevenoldstyle eightoldstyle nineoldstyle commasuperior threequartersemdash periodsuperior
questionsmall asuperior bsuperior centsuperior dsuperior esuperior isuperior lsuperior msuperior nsuperior osuperior rsuperior
ssuperior tsuperior ff ffi ffl parenleftinferior parenrightinferior Circumflexsmall hyphensuperior Gravesmall Asmall Bsmall
Csmall Dsmall Esmall Fsmall Gsmall Hsmall Ismall Jsmall Ksmall Lsmall Msmall Nsmall
Osmall Psmall Qsmall Rsmall Ssmall Tsmall Usmall Vsmall Wsmall Xsmall Ysmall Zsmall
colonmonetary onefitted rupiah Tildesmall exclamdownsmall centoldstyle Lslashsmall Scaronsmall Zcaronsmall Dieresissmall Brevesmall Caronsmall
Dotaccentsmall Macronsmall figuredash hypheninferior Ogoneksmall Ringsmall Cedillasmall questiondownsmall oneeighth threeeighths fiveeighths seveneighths
onethird twothirds zerosuperior foursuperior fivesuperior sixsuperior sevensuperior eightsuperior ninesuperior zeroinferior oneinferior twoinferior
threeinferior fourinferior fiveinferior sixinferior seveninferior eightinferior nineinferior centinferior dollarinferior periodinferior commainferior Agravesmall
Aacutesmall Acircumflexsmall Atildesmall Adieresissmall Aringsmall AEsmall Ccedillasmall Egravesmall Eacutesmall Ecircumflexsmall Edieresissmall Igravesmall
Iacutesmall Icircumflexsmall Idieresissmall Ethsmall Ntildesmall Ogravesmall Oacutesmall Ocircumflexsmall Otildesmall Odieresissmall OEsmall Oslashsmall
Ugravesmall Uacutesmall Ucircumflexsmall Udieresissmall Yacutesmall Thornsmall Ydieresissmall 001.000 001.001 001.002 001.003 Black
Bold Book Light Medium Regular Roman Semibold
`)

// cffStandardUnicode 按SID保存标准字形的单字符Unicode映射，不替换实际字形
// https://github.com/adobe-type-tools/agl-aglfn
var cffStandardUnicode = [...]rune{
	0x0, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x2019, 0x28, 0x29, 0x2A,
	0x2B, 0x2C, 0x2D, 0x2E, 0x2F, 0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36,
	0x37, 0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E, 0x3F, 0x40, 0x41, 0x42,
	0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4E,
	0x4F, 0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5A,
	0x5B, 0x5C, 0x5D, 0x5E, 0x5F, 0x2018, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66,
	0x67, 0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x6F, 0x70, 0x71, 0x72,
	0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7A, 0x7B, 0x7C, 0x7D, 0x7E,
	0xA1, 0xA2, 0xA3, 0x2044, 0xA5, 0x192, 0xA7, 0xA4, 0x27, 0x201C, 0xAB, 0x2039,
	0x203A, 0xFB01, 0xFB02, 0x2013, 0x2020, 0x2021, 0xB7, 0xB6, 0x2022, 0x201A, 0x201E, 0x201D,
	0xBB, 0x2026, 0x2030, 0xBF, 0x60, 0xB4, 0x2C6, 0x2DC, 0xAF, 0x2D8, 0x2D9, 0xA8,
	0x2DA, 0xB8, 0x2DD, 0x2DB, 0x2C7, 0x2014, 0xC6, 0xAA, 0x141, 0xD8, 0x152, 0xBA,
	0xE6, 0x131, 0x142, 0xF8, 0x153, 0xDF, 0xB9, 0xAC, 0xB5, 0x2122, 0xD0, 0xBD,
	0xB1, 0xDE, 0xBC, 0xF7, 0xA6, 0xB0, 0xFE, 0xBE, 0xB2, 0xAE, 0x2212, 0xF0,
	0xD7, 0xB3, 0xA9, 0xC1, 0xC2, 0xC4, 0xC0, 0xC5, 0xC3, 0xC7, 0xC9, 0xCA,
	0xCB, 0xC8, 0xCD, 0xCE, 0xCF, 0xCC, 0xD1, 0xD3, 0xD4, 0xD6, 0xD2, 0xD5,
	0x160, 0xDA, 0xDB, 0xDC, 0xD9, 0xDD, 0x178, 0x17D, 0xE1, 0xE2, 0xE4, 0xE0,
	0xE5, 0xE3, 0xE7, 0xE9, 0xEA, 0xEB, 0xE8, 0xED, 0xEE, 0xEF, 0xEC, 0xF1,
	0xF3, 0xF4, 0xF6, 0xF2, 0xF5, 0x161, 0xFA, 0xFB, 0xFC, 0xF9, 0xFD, 0xFF,
	0x17E, 0xF721, 0xF6F8, 0xF724, 0xF6E4, 0xF726, 0xF7B4, 0x207D, 0x207E, 0x2025, 0x2024, 0xF730,
	0xF731, 0xF732, 0xF733, 0xF734, 0xF735, 0xF736, 0xF737, 0xF738, 0xF739, 0xF6E2, 0xF6DE, 0xF6E8,
	0xF73F, 0xF6E9, 0xF6EA, 0xF6E0, 0xF6EB, 0xF6EC, 0xF6ED, 0xF6EE, 0xF6EF, 0x207F, 0xF6F0, 0xF6F1,
	0xF6F2, 0xF6F3, 0xFB00, 0xFB03, 0xFB04, 0x208D, 0x208E, 0xF6F6, 0xF6E6, 0xF760, 0xF761, 0xF762,
	0xF763, 0xF764, 0xF765, 0xF766, 0xF767, 0xF768, 0xF769, 0xF76A, 0xF76B, 0xF76C, 0xF76D, 0xF76E,
	0xF76F, 0xF770, 0xF771, 0xF772, 0xF773, 0xF774, 0xF775, 0xF776, 0xF777, 0xF778, 0xF779, 0xF77A,
	0x20A1, 0xF6DC, 0xF6DD, 0xF6FE, 0xF7A1, 0xF7A2, 0xF6F9, 0xF6FD, 0xF6FF, 0xF7A8, 0xF6F4, 0xF6F5,
	0xF6F7, 0xF7AF, 0x2012, 0xF6E5, 0xF6FB, 0xF6FC, 0xF7B8, 0xF7BF, 0x215B, 0x215C, 0x215D, 0x215E,
	0x2153, 0x2154, 0x2070, 0x2074, 0x2075, 0x2076, 0x2077, 0x2078, 0x2079, 0x2080, 0x2081, 0x2082,
	0x2083, 0x2084, 0x2085, 0x2086, 0x2087, 0x2088, 0x2089, 0xF6DF, 0xF6E3, 0xF6E7, 0xF6E1, 0xF7E0,
	0xF7E1, 0xF7E2, 0xF7E3, 0xF7E4, 0xF7E5, 0xF7E6, 0xF7E7, 0xF7E8, 0xF7E9, 0xF7EA, 0xF7EB, 0xF7EC,
	0xF7ED, 0xF7EE, 0xF7EF, 0xF7F0, 0xF7F1, 0xF7F2, 0xF7F3, 0xF7F4, 0xF7F5, 0xF7F6, 0xF6FA, 0xF7F8,
	0xF7F9, 0xF7FA, 0xF7FB, 0xF7FC, 0xF7FD, 0xF7FE, 0xF7FF, 0x0, 0x0, 0x0, 0x0, 0x0,
	0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0,
}

// cffStandardNameUnicode 按需定位CFF标准字形的Unicode字符
var cffStandardNameUnicode = func() map[string]rune {
	result := make(map[string]rune, len(cffStandardStrings))
	for sid, character := range cffStandardUnicode {
		if character != 0 {
			result[cffStandardStrings[sid]] = character
		}
	}
	return result
}()
