package calendar

import "time"

func dayBounds(loc *time.Location, now time.Time) (start, end time.Time) {
	y, m, d := now.In(loc).Date()
	start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	end = start.Add(24 * time.Hour)
	return start, end
}

func repairWindow(loc *time.Location, now time.Time) (start, end time.Time) {
	y, m, d := now.In(loc).Date()
	start = time.Date(y, m, d, 8, 0, 0, 0, loc)
	end = time.Date(y, m, d, 20, 0, 0, 0, loc)
	return start, end
}
