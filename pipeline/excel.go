package pipeline

import (
	"fmt"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
)

const excelFile = "videos.xlsx"
const sheetName = "Videos"

type VideoRecord struct {
	Date         string
	Title        string
	Summary      string
	Description  string
	Keywords     []string
	Hashtags     []string
	Short1Title  string
	Short1Keys   []string
	Short2Title  string
	Short2Keys   []string
	Short3Title  string
	Short3Keys   []string
	Status       string
	Folder       string
}

var headers = []string{
	"Date", "Title", "Summary", "Description",
	"Keywords", "Hashtags",
	"Short 1 Title", "Short 1 Keywords",
	"Short 2 Title", "Short 2 Keywords",
	"Short 3 Title", "Short 3 Keywords",
	"Status", "Output Folder",
}

func LogToExcel(record VideoRecord) error {
	var f *excelize.File

	if _, err := os.Stat(excelFile); os.IsNotExist(err) {
		f = excelize.NewFile()
		f.SetSheetName("Sheet1", sheetName)
		for i, h := range headers {
			cell, _ := excelize.CoordinatesToCellName(i+1, 1)
			f.SetCellValue(sheetName, cell, h)
		}
		style, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
			Fill: excelize.Fill{Type: "pattern", Color: []string{"4472C4"}, Pattern: 1},
		})
		endCell, _ := excelize.CoordinatesToCellName(len(headers), 1)
		f.SetCellStyle(sheetName, "A1", endCell, style)
	} else {
		var err error
		f, err = excelize.OpenFile(excelFile)
		if err != nil {
			return fmt.Errorf("failed to open Excel file: %w", err)
		}
	}
	defer f.Close()

	rows, err := f.GetRows(sheetName)
	if err != nil {
		return fmt.Errorf("failed to get rows: %w", err)
	}
	nextRow := len(rows) + 1

	values := []interface{}{
		record.Date,
		record.Title,
		record.Summary,
		record.Description,
		strings.Join(record.Keywords, ", "),
		strings.Join(record.Hashtags, " "),
		record.Short1Title,
		strings.Join(record.Short1Keys, ", "),
		record.Short2Title,
		strings.Join(record.Short2Keys, ", "),
		record.Short3Title,
		strings.Join(record.Short3Keys, ", "),
		record.Status,
		record.Folder,
	}

	for i, v := range values {
		cell, _ := excelize.CoordinatesToCellName(i+1, nextRow)
		f.SetCellValue(sheetName, cell, v)
	}

	// column widths
	widths := map[string]float64{
		"A": 12, "B": 50, "C": 40, "D": 60,
		"E": 50, "F": 40,
		"G": 45, "H": 50,
		"I": 45, "J": 50,
		"K": 45, "L": 50,
		"M": 12, "N": 35,
	}
	for col, width := range widths {
		f.SetColWidth(sheetName, col, col, width)
	}

	// wrap text for description column
	wrapStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"},
	})
	descCell, _ := excelize.CoordinatesToCellName(4, nextRow)
	f.SetCellStyle(sheetName, descCell, descCell, wrapStyle)

	return f.SaveAs(excelFile)
}
