// Copyright 2016 - 2025 The excelize Authors. All rights reserved. Use of
// this source code is governed by a BSD-style license that can be found in
// the LICENSE file.
//
// Package excelize providing a set of functions that allow you to write to and
// read from XLAM / XLSM / XLSX / XLTM / XLTX files. Supports reading and
// writing spreadsheet documents generated by Microsoft Excel™ 2007 and later.
// Supports complex components by high compatibility, and provided streaming
// API for generating or reading data from a worksheet with huge amounts of
// data. This library needs Go version 1.23 or later.

package excelize

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"

	"github.com/tiendc/go-deepcopy"
)

// Define the default cell size and EMU unit of measurement.
const (
	defaultColWidth        float64 = 10.5
	defaultColWidthPixels  float64 = 84.0
	defaultRowHeight       float64 = 15.6
	defaultRowHeightPixels float64 = 20.8
	EMU                    int     = 9525
)

// Cols defines an iterator to a sheet
type Cols struct {
	err                                    error
	curCol, totalCols, totalRows, stashCol int
	rawCellValue                           bool
	sheet                                  string
	f                                      *File
	sheetXML                               []byte
	sst                                    *xlsxSST
}

// GetCols gets the value of all cells by columns on the worksheet based on the
// given worksheet name, returned as a two-dimensional array, where the value
// of the cell is converted to the `string` type. If the cell format can be
// applied to the value of the cell, the applied value will be used, otherwise
// the original value will be used.
//
// For example, get and traverse the value of all cells by columns on a
// worksheet named
// 'Sheet1':
//
//	cols, err := f.GetCols("Sheet1")
//	if err != nil {
//	    fmt.Println(err)
//	    return
//	}
//	for _, col := range cols {
//	    for _, rowCell := range col {
//	        fmt.Print(rowCell, "\t")
//	    }
//	    fmt.Println()
//	}
func (f *File) GetCols(sheet string, opts ...Options) ([][]string, error) {
	cols, err := f.Cols(sheet)
	if err != nil {
		return nil, err
	}
	results := make([][]string, 0, 64)
	for cols.Next() {
		col, _ := cols.Rows(opts...)
		results = append(results, col)
	}
	return results, nil
}

// Next will return true if the next column is found.
func (cols *Cols) Next() bool {
	cols.curCol++
	return cols.curCol <= cols.totalCols
}

// Error will return an error when the error occurs.
func (cols *Cols) Error() error {
	return cols.err
}

// Rows return the current column's row values.
func (cols *Cols) Rows(opts ...Options) ([]string, error) {
	var rowIterator rowXMLIterator
	if cols.stashCol >= cols.curCol {
		return rowIterator.cells, rowIterator.err
	}
	cols.rawCellValue = cols.f.getOptions(opts...).RawCellValue
	if cols.sst, rowIterator.err = cols.f.sharedStringsReader(); rowIterator.err != nil {
		return rowIterator.cells, rowIterator.err
	}
	decoder := cols.f.xmlNewDecoder(bytes.NewReader(cols.sheetXML))
	for {
		token, _ := decoder.Token()
		if token == nil {
			break
		}
		switch xmlElement := token.(type) {
		case xml.StartElement:
			rowIterator.inElement = xmlElement.Name.Local
			if rowIterator.inElement == "row" {
				rowIterator.cellCol = 0
				rowIterator.cellRow++
				attrR, _ := attrValToInt("r", xmlElement.Attr)
				if attrR != 0 {
					rowIterator.cellRow = attrR
				}
			}
			if cols.rowXMLHandler(&rowIterator, &xmlElement, decoder); rowIterator.err != nil {
				return rowIterator.cells, rowIterator.err
			}
		case xml.EndElement:
			if xmlElement.Name.Local == "sheetData" {
				return rowIterator.cells, rowIterator.err
			}
		}
	}
	return rowIterator.cells, rowIterator.err
}

// columnXMLIterator defined runtime use field for the worksheet column SAX parser.
type columnXMLIterator struct {
	err                  error
	cols                 Cols
	cellCol, curRow, row int
}

// columnXMLHandler parse the column XML element of the worksheet.
func columnXMLHandler(colIterator *columnXMLIterator, xmlElement *xml.StartElement) {
	colIterator.err = nil
	inElement := xmlElement.Name.Local
	if inElement == "row" {
		colIterator.row++
		for _, attr := range xmlElement.Attr {
			if attr.Name.Local == "r" {
				if colIterator.curRow, colIterator.err = strconv.Atoi(attr.Value); colIterator.err != nil {
					return
				}
				colIterator.row = colIterator.curRow
			}
		}
		colIterator.cols.totalRows = colIterator.row
		colIterator.cellCol = 0
	}
	if inElement == "c" {
		colIterator.cellCol++
		for _, attr := range xmlElement.Attr {
			if attr.Name.Local == "r" {
				if colIterator.cellCol, _, colIterator.err = CellNameToCoordinates(attr.Value); colIterator.err != nil {
					return
				}
			}
		}
		if colIterator.cellCol > colIterator.cols.totalCols {
			colIterator.cols.totalCols = colIterator.cellCol
		}
	}
}

// rowXMLHandler parse the row XML element of the worksheet.
func (cols *Cols) rowXMLHandler(rowIterator *rowXMLIterator, xmlElement *xml.StartElement, decoder *xml.Decoder) {
	if rowIterator.inElement == "c" {
		rowIterator.cellCol++
		for _, attr := range xmlElement.Attr {
			if attr.Name.Local == "r" {
				if rowIterator.cellCol, rowIterator.cellRow, rowIterator.err = CellNameToCoordinates(attr.Value); rowIterator.err != nil {
					return
				}
			}
		}
		blank := rowIterator.cellRow - len(rowIterator.cells)
		for i := 1; i < blank; i++ {
			rowIterator.cells = append(rowIterator.cells, "")
		}
		if rowIterator.cellCol == cols.curCol {
			colCell := xlsxC{}
			_ = decoder.DecodeElement(&colCell, xmlElement)
			val, _ := colCell.getValueFrom(cols.f, cols.sst, cols.rawCellValue)
			rowIterator.cells = append(rowIterator.cells, val)
		}
	}
}

// Cols returns a columns iterator, used for streaming reading data for a
// worksheet with a large data. This function is concurrency safe. For
// example:
//
//	cols, err := f.Cols("Sheet1")
//	if err != nil {
//	    fmt.Println(err)
//	    return
//	}
//	for cols.Next() {
//	    col, err := cols.Rows()
//	    if err != nil {
//	        fmt.Println(err)
//	    }
//	    for _, rowCell := range col {
//	        fmt.Print(rowCell, "\t")
//	    }
//	    fmt.Println()
//	}
func (f *File) Cols(sheet string) (*Cols, error) {
	if err := checkSheetName(sheet); err != nil {
		return nil, err
	}
	name, ok := f.getSheetXMLPath(sheet)
	if !ok {
		return nil, ErrSheetNotExist{sheet}
	}
	if worksheet, ok := f.Sheet.Load(name); ok && worksheet != nil {
		ws := worksheet.(*xlsxWorksheet)
		ws.mu.Lock()
		defer ws.mu.Unlock()
		output, _ := xml.Marshal(ws)
		f.saveFileList(name, f.replaceNameSpaceBytes(name, output))
	}
	var colIterator columnXMLIterator
	colIterator.cols.sheetXML = f.readBytes(name)
	decoder := f.xmlNewDecoder(bytes.NewReader(colIterator.cols.sheetXML))
	for {
		token, _ := decoder.Token()
		if token == nil {
			break
		}
		switch xmlElement := token.(type) {
		case xml.StartElement:
			columnXMLHandler(&colIterator, &xmlElement)
			if colIterator.err != nil {
				return &colIterator.cols, colIterator.err
			}
		case xml.EndElement:
			if xmlElement.Name.Local == "sheetData" {
				colIterator.cols.f = f
				colIterator.cols.sheet = sheet
				return &colIterator.cols, nil
			}
		}
	}
	return &colIterator.cols, nil
}

// GetColVisible provides a function to get visible of a single column by given
// worksheet name and column name. This function is concurrency safe. For
// example, get visible state of column D in Sheet1:
//
//	visible, err := f.GetColVisible("Sheet1", "D")
func (f *File) GetColVisible(sheet, col string) (bool, error) {
	colNum, err := ColumnNameToNumber(col)
	if err != nil {
		return true, err
	}
	f.mu.Lock()
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		f.mu.Unlock()
		return false, err
	}
	f.mu.Unlock()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.Cols == nil {
		return true, err
	}
	visible := true
	for c := range ws.Cols.Col {
		colData := &ws.Cols.Col[c]
		if colData.Min <= colNum && colNum <= colData.Max {
			visible = !colData.Hidden
		}
	}
	return visible, err
}

// SetColVisible provides a function to set visible columns by given worksheet
// name, columns range and visibility. This function is concurrency safe.
//
// For example hide column D on Sheet1:
//
//	err := f.SetColVisible("Sheet1", "D", false)
//
// Hide the columns from D to F (included):
//
//	err := f.SetColVisible("Sheet1", "D:F", false)
func (f *File) SetColVisible(sheet, columns string, visible bool) error {
	minVal, maxVal, err := f.parseColRange(columns)
	if err != nil {
		return err
	}
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	colData := xlsxCol{
		Min:         minVal,
		Max:         maxVal,
		Width:       float64Ptr(defaultColWidth),
		Hidden:      !visible,
		CustomWidth: true,
	}
	if ws.Cols == nil {
		cols := xlsxCols{}
		cols.Col = append(cols.Col, colData)
		ws.Cols = &cols
		return nil
	}
	ws.Cols.Col = flatCols(colData, ws.Cols.Col, func(fc, c xlsxCol) xlsxCol {
		fc.BestFit = c.BestFit
		fc.Collapsed = c.Collapsed
		fc.CustomWidth = c.CustomWidth
		fc.OutlineLevel = c.OutlineLevel
		fc.Phonetic = c.Phonetic
		fc.Style = c.Style
		fc.Width = c.Width
		return fc
	})
	return nil
}

// GetColOutlineLevel provides a function to get outline level of a single
// column by given worksheet name and column name. For example, get outline
// level of column D in Sheet1:
//
//	level, err := f.GetColOutlineLevel("Sheet1", "D")
func (f *File) GetColOutlineLevel(sheet, col string) (uint8, error) {
	level := uint8(0)
	colNum, err := ColumnNameToNumber(col)
	if err != nil {
		return level, err
	}
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		return 0, err
	}
	if ws.Cols == nil {
		return level, err
	}
	for c := range ws.Cols.Col {
		colData := &ws.Cols.Col[c]
		if colData.Min <= colNum && colNum <= colData.Max {
			level = colData.OutlineLevel
		}
	}
	return level, err
}

// parseColRange parse and convert column range with column name to the column number.
func (f *File) parseColRange(columns string) (minVal, maxVal int, err error) {
	colsTab := strings.Split(columns, ":")
	minVal, err = ColumnNameToNumber(colsTab[0])
	if err != nil {
		return
	}
	maxVal = minVal
	if len(colsTab) == 2 {
		if maxVal, err = ColumnNameToNumber(colsTab[1]); err != nil {
			return
		}
	}
	if maxVal < minVal {
		minVal, maxVal = maxVal, minVal
	}
	return
}

// SetColOutlineLevel provides a function to set outline level of a single
// column by given worksheet name and column name. The value of parameter
// 'level' is 1-7. For example, set outline level of column D in Sheet1 to 2:
//
//	err := f.SetColOutlineLevel("Sheet1", "D", 2)
func (f *File) SetColOutlineLevel(sheet, col string, level uint8) error {
	if level > 7 || level < 1 {
		return ErrOutlineLevel
	}
	colNum, err := ColumnNameToNumber(col)
	if err != nil {
		return err
	}
	colData := xlsxCol{
		Min:          colNum,
		Max:          colNum,
		OutlineLevel: level,
		CustomWidth:  true,
	}
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	if ws.Cols == nil {
		cols := xlsxCols{}
		cols.Col = append(cols.Col, colData)
		ws.Cols = &cols
		return err
	}
	ws.Cols.Col = flatCols(colData, ws.Cols.Col, func(fc, c xlsxCol) xlsxCol {
		fc.BestFit = c.BestFit
		fc.Collapsed = c.Collapsed
		fc.CustomWidth = c.CustomWidth
		fc.Hidden = c.Hidden
		fc.Phonetic = c.Phonetic
		fc.Style = c.Style
		fc.Width = c.Width
		return fc
	})
	return err
}

// SetColStyle provides a function to set style of columns by given worksheet
// name, columns range and style ID. This function is concurrency safe. Note
// that this will overwrite the existing styles for the columns, it won't
// append or merge style with existing styles.
//
// For example set style of column H on Sheet1:
//
//	err = f.SetColStyle("Sheet1", "H", style)
//
// Set style of columns C:F on Sheet1:
//
//	err = f.SetColStyle("Sheet1", "C:F", style)
func (f *File) SetColStyle(sheet, columns string, styleID int) error {
	minVal, maxVal, err := f.parseColRange(columns)
	if err != nil {
		return err
	}
	f.mu.Lock()
	s, err := f.stylesReader()
	if err != nil {
		f.mu.Unlock()
		return err
	}
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	s.mu.Lock()
	if styleID < 0 || s.CellXfs == nil || len(s.CellXfs.Xf) <= styleID {
		s.mu.Unlock()
		return newInvalidStyleID(styleID)
	}
	s.mu.Unlock()
	ws.mu.Lock()
	ws.setColStyle(minVal, maxVal, styleID)
	ws.mu.Unlock()
	if rows := len(ws.SheetData.Row); rows > 0 {
		for col := minVal; col <= maxVal; col++ {
			from, _ := CoordinatesToCellName(col, 1)
			to, _ := CoordinatesToCellName(col, rows)
			err = f.SetCellStyle(sheet, from, to, styleID)
		}
	}
	return err
}

// setColStyle provides a function to set the style of a single column or
// multiple columns.
func (ws *xlsxWorksheet) setColStyle(minVal, maxVal, styleID int) {
	if ws.Cols == nil {
		ws.Cols = &xlsxCols{}
	}
	width := defaultColWidth
	if ws.SheetFormatPr != nil && ws.SheetFormatPr.DefaultColWidth > 0 {
		width = ws.SheetFormatPr.DefaultColWidth
	}
	ws.Cols.Col = flatCols(xlsxCol{
		Min:   minVal,
		Max:   maxVal,
		Width: float64Ptr(width),
		Style: styleID,
	}, ws.Cols.Col, func(fc, c xlsxCol) xlsxCol {
		fc.BestFit = c.BestFit
		fc.Collapsed = c.Collapsed
		fc.CustomWidth = c.CustomWidth
		fc.Hidden = c.Hidden
		fc.OutlineLevel = c.OutlineLevel
		fc.Phonetic = c.Phonetic
		fc.Width = c.Width
		return fc
	})
}

// SetColWidth provides a function to set the width of a single column or
// multiple columns. This function is concurrency safe. For example:
//
//	err := f.SetColWidth("Sheet1", "A", "H", 20)
func (f *File) SetColWidth(sheet, startCol, endCol string, width float64) error {
	minVal, maxVal, err := f.parseColRange(startCol + ":" + endCol)
	if err != nil {
		return err
	}
	if width > MaxColumnWidth {
		return ErrColumnWidth
	}
	f.mu.Lock()
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.setColWidth(minVal, maxVal, width)
	return err
}

// setColWidth provides a function to set the width of a single column or
// multiple columns.
func (ws *xlsxWorksheet) setColWidth(minVal, maxVal int, width float64) {
	col := xlsxCol{
		Min:         minVal,
		Max:         maxVal,
		Width:       float64Ptr(width),
		CustomWidth: true,
	}
	if ws.Cols == nil {
		cols := xlsxCols{}
		cols.Col = append(cols.Col, col)
		ws.Cols = &cols
		return
	}
	ws.Cols.Col = flatCols(col, ws.Cols.Col, func(fc, c xlsxCol) xlsxCol {
		fc.BestFit = c.BestFit
		fc.Collapsed = c.Collapsed
		fc.Hidden = c.Hidden
		fc.OutlineLevel = c.OutlineLevel
		fc.Phonetic = c.Phonetic
		fc.Style = c.Style
		return fc
	})
}

// flatCols provides a method for the column's operation functions to flatten
// and check the worksheet columns.
func flatCols(col xlsxCol, cols []xlsxCol, replacer func(fc, c xlsxCol) xlsxCol) []xlsxCol {
	var fc []xlsxCol
	for i := col.Min; i <= col.Max; i++ {
		var c xlsxCol
		deepcopy.Copy(&c, col)
		c.Min, c.Max = i, i
		fc = append(fc, c)
	}
	inFlat := func(colID int, cols []xlsxCol) (int, bool) {
		for idx, c := range cols {
			if c.Max == colID && c.Min == colID {
				return idx, true
			}
		}
		return -1, false
	}
	for _, column := range cols {
		for i := column.Min; i <= column.Max; i++ {
			if idx, ok := inFlat(i, fc); ok {
				fc[idx] = replacer(fc[idx], column)
				continue
			}
			var c xlsxCol
			deepcopy.Copy(&c, column)
			c.Min, c.Max = i, i
			fc = append(fc, c)
		}
	}
	return fc
}

// positionObjectPixels calculate the vertices that define the position of a
// graphical object within the worksheet in pixels.
//
//	      +------------+------------+
//	      |     A      |      B     |
//	+-----+------------+------------+
//	|     |(x1,y1)     |            |
//	|  1  |(A1)._______|______      |
//	|     |    |              |     |
//	|     |    |              |     |
//	+-----+----|    OBJECT    |-----+
//	|     |    |              |     |
//	|  2  |    |______________.     |
//	|     |            |        (B2)|
//	|     |            |     (x2,y2)|
//	+-----+------------+------------+
//
// Example of an object that covers some range reference from cell A1 to B2.
//
// Based on the width and height of the object we need to calculate 8 vars:
//
//	colStart, rowStart, colEnd, rowEnd, x1, y1, x2, y2.
//
// We also calculate the absolute x and y position of the top left vertex of
// the object. This is required for images.
//
// The width and height of the cells that the object occupies can be
// variable and have to be taken into account.
//
// The values of col_start and row_start are passed in from the calling
// function. The values of col_end and row_end are calculated by
// subtracting the width and height of the object from the width and
// height of the underlying cells.
//
//	colStart        # Col containing upper left corner of object.
//	x1              # Distance to left side of object.
//
//	rowStart        # Row containing top left corner of object.
//	y1              # Distance to top of object.
//
//	colEnd          # Col containing lower right corner of object.
//	x2              # Distance to right side of object.
//
//	rowEnd          # Row containing bottom right corner of object.
//	y2              # Distance to bottom of object.
//
//	width           # Width of object frame.
//	height          # Height of object frame.
func (f *File) positionObjectPixels(sheet string, col, row, width, height int, opts *GraphicOptions) (int, int, int, int, int, int, int, int) {
	colIdx, rowIdx := col-1, row-1
	// Initialized end cell to the same as the start cell.
	colEnd, rowEnd := colIdx, rowIdx
	x1, y1, x2, y2 := opts.OffsetX, opts.OffsetY, width, height
	if opts.Positioning == "" || opts.Positioning == "twoCell" {
		// Using a twoCellAnchor, the maximum possible offset is limited by the
		// "from" cell dimensions. If these were to be exceeded the "toPoint" would
		// be calculated incorrectly, since the requested "fromPoint" is not possible

		x1 = min(x1, f.getColWidth(sheet, col))
		y1 = min(y1, f.getRowHeight(sheet, row))

		x2 += x1
		y2 += y1
		// Subtract the underlying cell widths to find end cell of the object.
		for x2 >= f.getColWidth(sheet, colEnd+1) {
			colEnd++
			x2 -= f.getColWidth(sheet, colEnd)
		}

		// Subtract the underlying cell heights to find end cell of the object.
		for y2 >= f.getRowHeight(sheet, rowEnd+1) {
			rowEnd++
			y2 -= f.getRowHeight(sheet, rowEnd)
		}
	}
	// The end vertices are whatever is left from the width and height.
	return colIdx, rowIdx, colEnd, rowEnd, x1, y1, x2, y2
}

// getColWidth provides a function to get column width in pixels by given
// sheet name and column number.
func (f *File) getColWidth(sheet string, col int) int {
	ws, _ := f.workSheetReader(sheet)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.Cols != nil {
		width := -1.0
		for _, v := range ws.Cols.Col {
			if v.Min <= col && col <= v.Max && v.Width != nil {
				width = *v.Width
				break
			}
		}
		if width != -1.0 {
			return int(convertColWidthToPixels(width))
		}
	}
	if ws.SheetFormatPr != nil && ws.SheetFormatPr.DefaultColWidth > 0 {
		return int(convertColWidthToPixels(ws.SheetFormatPr.DefaultColWidth))
	}
	// Optimization for when the column widths haven't changed.
	return int(defaultColWidthPixels)
}

// GetColStyle provides a function to get column style ID by given worksheet
// name and column name. This function is concurrency safe.
func (f *File) GetColStyle(sheet, col string) (int, error) {
	var styleID int
	colNum, err := ColumnNameToNumber(col)
	if err != nil {
		return styleID, err
	}
	f.mu.Lock()
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		f.mu.Unlock()
		return styleID, err
	}
	f.mu.Unlock()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.Cols != nil {
		for _, v := range ws.Cols.Col {
			if v.Min <= colNum && colNum <= v.Max {
				styleID = v.Style
			}
		}
	}
	return styleID, err
}

// GetColWidth provides a function to get column width by given worksheet name
// and column name. This function is concurrency safe.
func (f *File) GetColWidth(sheet, col string) (float64, error) {
	colNum, err := ColumnNameToNumber(col)
	if err != nil {
		return defaultColWidth, err
	}
	f.mu.Lock()
	ws, err := f.workSheetReader(sheet)
	if err != nil {
		f.mu.Unlock()
		return defaultColWidth, err
	}
	f.mu.Unlock()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.Cols != nil {
		var width float64
		for _, v := range ws.Cols.Col {
			if v.Min <= colNum && colNum <= v.Max && v.Width != nil {
				width = *v.Width
			}
		}
		if width != 0 {
			return width, err
		}
	}
	if ws.SheetFormatPr != nil && ws.SheetFormatPr.DefaultColWidth > 0 {
		return ws.SheetFormatPr.DefaultColWidth, err
	}
	// Optimization for when the column widths haven't changed.
	return defaultColWidth, err
}

// InsertCols provides a function to insert new columns before the given column
// name and number of columns. For example, create two columns before column
// C in Sheet1:
//
//	err := f.InsertCols("Sheet1", "C", 2)
//
// Use this method with caution, which will affect changes in references such
// as formulas, charts, and so on. If there is any referenced value of the
// worksheet, it will cause a file error when you open it. The excelize only
// partially updates these references currently.
func (f *File) InsertCols(sheet, col string, n int) error {
	num, err := ColumnNameToNumber(col)
	if err != nil {
		return err
	}
	if n < 1 || n > MaxColumns {
		return ErrColumnNumber
	}
	return f.adjustHelper(sheet, columns, num, n)
}

// RemoveCol provides a function to remove single column by given worksheet
// name and column index. For example, remove column C in Sheet1:
//
//	err := f.RemoveCol("Sheet1", "C")
//
// Use this method with caution, which will affect changes in references such
// as formulas, charts, and so on. If there is any referenced value of the
// worksheet, it will cause a file error when you open it. The excelize only
// partially updates these references currently.
func (f *File) RemoveCol(sheet, col string) error {
	num, err := ColumnNameToNumber(col)
	if err != nil {
		return err
	}

	ws, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	ws.formulaSI.Clear()
	for rowIdx := range ws.SheetData.Row {
		rowData := &ws.SheetData.Row[rowIdx]
		for colIdx := range rowData.C {
			colName, _, _ := SplitCellName(rowData.C[colIdx].R)
			if colName == col {
				rowData.C = append(rowData.C[:colIdx], rowData.C[colIdx+1:]...)[:len(rowData.C)-1]
				break
			}
		}
	}
	return f.adjustHelper(sheet, columns, num, -1)
}

// convertColWidthToPixels provides function to convert the width of a cell
// from user's units to pixels. Excel rounds the column width to the nearest
// pixel. If the width hasn't been set by the user we use the default value.
// If the column is hidden it has a value of zero.
func convertColWidthToPixels(width float64) float64 {
	var pixels float64
	var maxDigitWidth float64 = 8
	if width == 0 {
		return pixels
	}
	if width < 1 {
		pixels = (width * 12) + 0.5
		return float64(int(pixels))
	}
	pixels = (width*maxDigitWidth + 0.5)
	return float64(int(pixels))
}
