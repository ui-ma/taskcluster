audience: general
level: patch
reference: issue 8525
---
Fix two related races that could leave a Taskcluster task in an inconsistent state when a Pulse publish failed during task creation or a run state transition:

- A run could transition to `pending` in `tasks.runs` without a corresponding `queue_pending_tasks` row, making the task invisible to workers and to the "pending tasks" UI/API counts. Transitions of a run to `pending` (`schedule_task`, `rerun_task`, `resolve_task`, `check_task_claim`) now commit the `queue_pending_tasks` row in the same database transaction as the `tasks.runs` update.
- A task could be inserted into `tasks` without a corresponding `queue_task_deadlines` row, leaving it untracked by the deadline resolver. `createTask` now inserts both rows atomically inside `create_task_atomic`.

Pulse `taskPending` / `taskException` / `taskDefined` publishes that follow these now-atomic DB commits are best-effort: the database is the source of truth, so a Pulse publish failure no longer fails the operation or leaves orphaned rows.
