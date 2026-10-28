package meetups

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tropa/internal/notify"
)

var monthsGen = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}

var weekdaysShort = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// When — время встречи по Алматы: «сб, 6 марта, 18:30»; год — только если не текущий.
func When(t time.Time) string {
	t = t.In(Almaty)
	s := fmt.Sprintf("%s, %d %s", weekdaysShort[t.Weekday()], t.Day(), monthsGen[t.Month()-1])
	if t.Year() != time.Now().In(Almaty).Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s + ", " + t.Format("15:04")
}

// Clock — «6 марта, 18:30» по Алматы: срок ответа на предложенное место.
func Clock(t time.Time) string {
	t = t.In(Almaty)
	return fmt.Sprintf("%d %s, %s", t.Day(), monthsGen[t.Month()-1], t.Format("15:04"))
}

// Тексты уведомлений. Простой текст: в Telegram уходят без разметки.

func (s *Service) link(m *Meetup) string { return s.SiteURL + m.URL() }

func (s *Service) offerText(m *Meetup, until time.Time) string {
	return fmt.Sprintf("Освободилось место на встрече «%s» (%s, %s).\n\nПодтвердите до %s, иначе место уйдёт следующему в очереди:\n%s",
		m.Title, When(m.StartsAt), m.PlaceName, Clock(until), s.link(m))
}

func (s *Service) offerSoonText(m *Meetup, until time.Time) string {
	return fmt.Sprintf("Место на встрече «%s» держится за вами до %s. Подтвердите, если идёте:\n%s",
		m.Title, Clock(until), s.link(m))
}

func (s *Service) movedText(m *Meetup) string {
	return fmt.Sprintf("Встречу «%s» перенесли: теперь %s, %s.\n%s", m.Title, When(m.StartsAt), m.PlaceName, s.link(m))
}

func (s *Service) cancelledText(m *Meetup) string {
	return fmt.Sprintf("Встречу «%s» (%s) отменили.\n%s", m.Title, When(m.StartsAt), s.link(m))
}

func (s *Service) reminderText(m *Meetup) string {
	return fmt.Sprintf("Напоминание: завтра встреча «%s» — %s, %s.\n%s", m.Title, When(m.StartsAt), m.PlaceName, s.link(m))
}

// notifyAll ставит уведомление всем участникам встречи (и очереди), кроме организатора.
func (s *Service) notifyAll(ctx context.Context, tx pgx.Tx, m *Meetup, kind, text string) error {
	rows, err := tx.Query(ctx, `SELECT user_id FROM participants WHERE meetup_id = $1 AND user_id <> $2`, m.ID, m.OrganizerID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := notify.Enqueue(ctx, tx, id, kind, text, ""); err != nil {
			return err
		}
	}
	return nil
}

const (
	// OfferReminder — за сколько до конца предложения напомнить о нём.
	OfferReminder = 2 * time.Hour
	// MeetupReminder — за сколько до встречи напомнить участникам.
	MeetupReminder = 24 * time.Hour
)

// Remind ставит в очередь напоминания: о предложении, которое скоро истечёт,
// и о встрече через сутки. Каждое — один раз (ключ dedup с временем события:
// если встречу перенесут, напоминание придёт и о новом времени).
// Зовётся воркером раз в минуту.
func (s *Service) Remind(ctx context.Context) error {
	now := s.Now()
	type row struct {
		meetupID, userID int64
		at               time.Time
	}
	collect := func(sql string, args ...any) ([]row, error) {
		rows, err := s.DB.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(&x.meetupID, &x.userID, &x.at)
			return x, err
		})
	}

	offers, err := collect(
		`SELECT meetup_id, user_id, offer_expires_at FROM participants
		  WHERE status = 'offered' AND offer_expires_at > $1 AND offer_expires_at <= $2`,
		now, now.Add(OfferReminder))
	if err != nil {
		return err
	}
	// напоминаем тем, кто записался заранее: только что записавшемуся оно ни к чему
	soon, err := collect(
		`SELECT m.id, p.user_id, m.starts_at FROM meetups m JOIN participants p ON p.meetup_id = m.id
		  WHERE m.status <> 'cancelled' AND p.status = 'confirmed' AND p.user_id <> m.organizer_id
		    AND m.starts_at > $1 AND m.starts_at <= $2 AND p.joined_at < $3`,
		now, now.Add(MeetupReminder), now.Add(-time.Hour))
	if err != nil {
		return err
	}

	enqueue := func(x row, kind string, text func(*Meetup) string) error {
		m, err := s.Get(ctx, x.meetupID)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s:%d:%d:%d", kind, x.meetupID, x.userID, x.at.Unix())
		return notify.Enqueue(ctx, s.DB, x.userID, kind, text(m), key)
	}
	for _, x := range offers {
		until := x.at
		if err := enqueue(x, "offer_soon", func(m *Meetup) string { return s.offerSoonText(m, until) }); err != nil {
			return err
		}
	}
	for _, x := range soon {
		if err := enqueue(x, "reminder", s.reminderText); err != nil {
			return err
		}
	}
	return nil
}
