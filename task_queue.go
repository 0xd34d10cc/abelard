package main

import (
	"container/heap"
	"fmt"
	"sync"
	"time"

	"github.com/samber/lo"
)

type ScheduledTask struct {
	Task *Task
	At   time.Time
}

func NewScheduledTask(now time.Time, task *Task) *ScheduledTask {
	t := &ScheduledTask{Task: task}
	t.advance(now)
	return t
}

func (t *ScheduledTask) advance(now time.Time) {
	now = now.UTC()
	switch t.Task.When {
	case TaskPeriodMinute:
		t.At = now.Truncate(time.Minute).Add(time.Minute)
	case TaskPeriodHourly:
		t.At = now.Truncate(time.Hour).Add(time.Hour)
	case TaskPeriodDaily:
		t.At = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	default:
		panic(fmt.Sprintf("Unhandled task.When: %v", t.Task.When))
	}
}

func (t *ScheduledTask) String() string {
	return fmt.Sprintf(
		"%v %v (%v)",
		t.At.Format(time.RFC3339),
		t.Task.ID,
		t.Task.When,
	)
}

type TaskQueue struct {
	m sync.Mutex
	q []*ScheduledTask
}

func NewTaskQueue(tasks []Task) (*TaskQueue, error) {
	now := time.Now().UTC()
	queue := make([]*ScheduledTask, 0, len(tasks))
	for _, task := range tasks {
		if task.When == TaskPeriodManual {
			// Skip manual tasks
			continue
		}

		t := &ScheduledTask{
			Task: lo.ToPtr(task),
		}
		t.advance(now)

		queue = append(queue, t)
	}

	q := &TaskQueue{q: queue}
	heap.Init(q)
	return q, nil
}

func (q *TaskQueue) PopNextTask() *ScheduledTask {
	q.m.Lock()
	defer q.m.Unlock()
	return heap.Pop(q).(*ScheduledTask)
}

func (q *TaskQueue) Next() *ScheduledTask {
	q.m.Lock()
	defer q.m.Unlock()
	return q.q[0]
}

func (q *TaskQueue) PushTask(task *ScheduledTask) {
	task.advance(time.Now())

	q.m.Lock()
	defer q.m.Unlock()
	heap.Push(q, task)
}

func (q *TaskQueue) List() []ScheduledTask {
	q.m.Lock()
	defer q.m.Unlock()

	tasks := make([]ScheduledTask, 0, len(q.q))
	for _, task := range q.q {
		tasks = append(tasks, *task)
	}
	return tasks
}

// heap.Interface
func (q *TaskQueue) Len() int { return len(q.q) }

func (q *TaskQueue) Less(i, j int) bool {
	return q.q[i].At.Before(q.q[j].At)
}

func (q *TaskQueue) Swap(i, j int) {
	q.q[i], q.q[j] = q.q[j], q.q[i]
}

func (q *TaskQueue) Push(x any) {
	item := x.(*ScheduledTask)
	q.q = append(q.q, item)
}

func (q *TaskQueue) Pop() any {
	old := q.q
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // don't stop the GC from reclaiming the item eventually
	q.q = old[0 : n-1]
	return item
}
