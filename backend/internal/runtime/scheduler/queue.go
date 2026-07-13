package scheduler

// fairQueue keeps one FIFO queue per project and rotates projects after every
// dequeue. A noisy project can therefore consume only its fair turn while
// another project has pending work.
type fairQueue struct {
	byProject map[int64][]*workItem
	order     []int64
	length    int
}

func (queue *fairQueue) Push(item *workItem) {
	if queue.byProject == nil {
		queue.byProject = make(map[int64][]*workItem)
	}
	projectQueue := queue.byProject[item.projectID]
	if len(projectQueue) == 0 {
		queue.order = append(queue.order, item.projectID)
	}
	queue.byProject[item.projectID] = append(projectQueue, item)
	queue.length++
}

func (queue *fairQueue) Pop() *workItem {
	for len(queue.order) > 0 {
		projectID := queue.order[0]
		queue.order = queue.order[1:]
		projectQueue := queue.byProject[projectID]
		if len(projectQueue) == 0 {
			delete(queue.byProject, projectID)
			continue
		}
		item := projectQueue[0]
		projectQueue = projectQueue[1:]
		queue.length--
		if len(projectQueue) == 0 {
			delete(queue.byProject, projectID)
		} else {
			queue.byProject[projectID] = projectQueue
			queue.order = append(queue.order, projectID)
		}
		return item
	}
	return nil
}

func (queue *fairQueue) Remove(target *workItem) bool {
	if target == nil {
		return false
	}
	projectQueue := queue.byProject[target.projectID]
	for index, item := range projectQueue {
		if item != target {
			continue
		}
		projectQueue = append(projectQueue[:index], projectQueue[index+1:]...)
		queue.length--
		if len(projectQueue) == 0 {
			delete(queue.byProject, target.projectID)
			for orderIndex, projectID := range queue.order {
				if projectID == target.projectID {
					queue.order = append(queue.order[:orderIndex], queue.order[orderIndex+1:]...)
					break
				}
			}
		} else {
			queue.byProject[target.projectID] = projectQueue
		}
		return true
	}
	return false
}

func (queue *fairQueue) Drain() []*workItem {
	items := make([]*workItem, 0, queue.length)
	for {
		item := queue.Pop()
		if item == nil {
			break
		}
		items = append(items, item)
	}
	return items
}

func (queue *fairQueue) Len() int { return queue.length }
