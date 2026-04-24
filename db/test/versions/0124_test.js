import assert from 'assert';
import helper from '../helper.js';
import testing from '@taskcluster/lib-testing';
import taskcluster from '@taskcluster/client';

const THIS_VERSION = parseInt(/.*\/0*(\d+)_test\.js/.exec(import.meta.url)[1]);
const PREV_VERSION = THIS_VERSION - 1;

suite(testing.suiteName(), function() {
  helper.withDbForVersion();

  test('schedule_task atomically inserts into queue_pending_tasks after upgrade', async function() {
    await testing.resetDb({ testDbUrl: helper.dbUrl });
    await helper.upgradeTo(THIS_VERSION);

    const db = await helper.setupDb('queue');
    const taskId = 'abcDEFghiJKLmnoPQRstuv';
    const created = taskcluster.fromNow('0 hours');
    const deadline = taskcluster.fromNow('1 hour');
    const expires = taskcluster.fromNow('2 hours');

    await db.fns.create_task_projid(
      taskId, 'prov/wt', 'sched', 'proj', 'group-1',
      JSON.stringify([]), 'all-completed', JSON.stringify([]),
      'high', 5, created, deadline, expires,
      JSON.stringify([]), {}, {}, JSON.stringify([]), {},
    );

    await db.fns.schedule_task(taskId, 'scheduled');

    const rows = await helper.withDbClient(async client => {
      const { rows } = await client.query(
        'select task_queue_id, priority, run_id from queue_pending_tasks where task_id = $1',
        [taskId],
      );
      return rows;
    });
    assert.equal(rows.length, 1);
    assert.equal(rows[0].task_queue_id, 'prov/wt');
    assert.equal(rows[0].priority, 5);
    assert.equal(rows[0].run_id, 0);
  });

  test('downgrade removes queue_pending_tasks_add_for_task / create_task_atomic and reverts schedule_task', async function() {
    await testing.resetDb({ testDbUrl: helper.dbUrl });
    await helper.upgradeTo(THIS_VERSION);
    await helper.downgradeTo(PREV_VERSION);

    // After downgrade the new helpers should no longer exist in the DB
    const dropped = await helper.withDbClient(async client => {
      const { rows } = await client.query(`
        select proname
        from pg_proc
        where proname in ('queue_pending_tasks_add_for_task', 'create_task_atomic')
      `);
      return rows.map(r => r.proname);
    });
    assert.deepEqual(dropped, [], 'helpers should not exist after downgrade');

    // The four modified DB fns must revert to their pre-123 bodies, which
    // do NOT enqueue into queue_pending_tasks. Exercise schedule_task as a
    // representative: after downgrade it should update tasks.runs but leave
    // queue_pending_tasks empty.
    const db = await helper.setupDb('queue');
    const taskId = 'abcDEFghiJKLmnoPQRstuv';
    const created = taskcluster.fromNow('0 hours');
    const deadline = taskcluster.fromNow('1 hour');
    const expires = taskcluster.fromNow('2 hours');

    await db.fns.create_task_projid(
      taskId, 'prov/wt', 'sched', 'proj', 'group-1',
      JSON.stringify([]), 'all-completed', JSON.stringify([]),
      'high', 5, created, deadline, expires,
      JSON.stringify([]), {}, {}, JSON.stringify([]), {},
    );

    await db.fns.schedule_task(taskId, 'scheduled');

    const pendingRows = await helper.withDbClient(async client => {
      const { rows } = await client.query(
        'select count(*)::int as c from queue_pending_tasks where task_id = $1',
        [taskId],
      );
      return rows[0].c;
    });
    assert.equal(pendingRows, 0, 'post-downgrade schedule_task must not enqueue into queue_pending_tasks');
  });

  test('create_task_atomic inserts task and queue_task_deadlines atomically', async function() {
    await testing.resetDb({ testDbUrl: helper.dbUrl });
    await helper.upgradeTo(THIS_VERSION);

    const db = await helper.setupDb('queue');
    const taskId = 'atomicCreate0000000000';
    const created = taskcluster.fromNow('0 hours');
    const deadline = taskcluster.fromNow('1 hour');
    const expires = taskcluster.fromNow('2 hours');

    await db.fns.create_task_atomic(
      taskId, 'prov/wt', 'sched', 'proj', 'group-1',
      JSON.stringify([]), 'all-completed', JSON.stringify([]),
      'high', 5, created, deadline, expires,
      JSON.stringify([]), {}, {}, JSON.stringify([]), {},
      600,
    );

    const tasksRow = await helper.withDbClient(async client => {
      const { rows } = await client.query('select task_id from tasks where task_id = $1', [taskId]);
      return rows[0];
    });
    assert.equal(tasksRow.task_id, taskId);

    const deadlineRow = await helper.withDbClient(async client => {
      const { rows } = await client.query(
        'select task_group_id, task_id, scheduler_id, deadline, visible from queue_task_deadlines where task_id = $1',
        [taskId],
      );
      return rows[0];
    });
    assert(deadlineRow, 'queue_task_deadlines row must be present after create_task_atomic');
    assert.equal(deadlineRow.task_id, taskId);
    assert.equal(deadlineRow.task_group_id, 'group-1');
    assert.equal(deadlineRow.scheduler_id, 'sched');
    const expectedVisibleMs = deadlineRow.deadline.getTime() + 600 * 1000;
    assert.equal(deadlineRow.visible.getTime(), expectedVisibleMs);
  });

  test('create_task_atomic tolerates a pre-existing deadline row via ON CONFLICT', async function() {
    await testing.resetDb({ testDbUrl: helper.dbUrl });
    await helper.upgradeTo(THIS_VERSION);

    const db = await helper.setupDb('queue');
    const taskId = 'preExistingDeadline00A';
    const created = taskcluster.fromNow('0 hours');
    const deadline = taskcluster.fromNow('1 hour');
    const expires = taskcluster.fromNow('2 hours');

    // Pre-insert a deadline row to simulate an orphan from the legacy flow.
    await helper.withDbClient(async client => {
      await client.query(
        'insert into queue_task_deadlines (task_group_id, task_id, scheduler_id, created, deadline, visible)' +
        " values ('group-1', $1, 'sched', now(), $2::timestamptz, $2::timestamptz + interval '10 minutes')",
        [taskId, deadline],
      );
    });

    await db.fns.create_task_atomic(
      taskId, 'prov/wt', 'sched', 'proj', 'group-1',
      JSON.stringify([]), 'all-completed', JSON.stringify([]),
      'high', 5, created, deadline, expires,
      JSON.stringify([]), {}, {}, JSON.stringify([]), {},
      600,
    );

    const taskCount = await helper.withDbClient(async client => {
      const { rows } = await client.query('select count(*)::int as c from tasks where task_id = $1', [taskId]);
      return rows[0].c;
    });
    assert.equal(taskCount, 1, 'task row must be inserted even with pre-existing deadline row');

    const deadlineCount = await helper.withDbClient(async client => {
      const { rows } = await client.query('select count(*)::int as c from queue_task_deadlines where task_id = $1', [taskId]);
      return rows[0].c;
    });
    assert.equal(deadlineCount, 1, 'pre-existing deadline row preserved (ON CONFLICT DO NOTHING)');
  });
});
