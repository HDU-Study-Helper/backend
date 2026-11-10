// Offline browser regression: every API is a synthetic fixture. No school or database calls.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const http = require("node:http");
const { chromium } = require(
  process.env.PLAYWRIGHT_MODULE || "playwright-core",
);
const root = path.resolve(__dirname, "../internal/users");
const source = {
  id: "fixture-user",
  username: "fixture",
  display_name: "测试用户",
  role: "user",
};
const sem = { academic_year: 2026, term: 1 };
const event = (id, title, day, extra = {}) => ({
  id,
  user_id: source.id,
  ...sem,
  teaching_week: 8,
  title,
  details: "地点 A101",
  start_time: `2026-10-${day}T06:00:00Z`,
  end_time: `2026-10-${day}T07:35:00Z`,
  source: "TIMETABLE",
  event_status: "INCOMPLETE",
  active: true,
  mod: { history: [] },
  ...extra,
});
(async () => {
  const server = http.createServer((req, res) => {
    const file =
      req.url === "/admin/schedules.js" ? "schedules.js" : "admin.html";
    res.setHeader(
      "Content-Type",
      file.endsWith(".js") ? "text/javascript" : "text/html; charset=utf-8",
    );
    res.setHeader(
      "Content-Security-Policy",
      "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
    );
    res.end(fs.readFileSync(path.join(root, file)));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}),
    args: ["--no-sandbox"],
  });
  const errors = [],
    checks = [];
  let requests = [],
    items = [
      event("math", "数学", 28, {
        mod: {
          history: [],
          weeks_raw: "2-16周(双)",
          week_pattern: "EVEN",
          teaching_weeks: [2, 4, 6, 8, 10, 12, 14, 16],
        },
      }),
      event("user", "个人事项", 29, { source: "USER" }),
      event("agent", "<img src=x onerror=alert(1)>", 30, { source: "AGENT" }),
    ],
    failSave = false,
    delayList = false,
    heldList = null;
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1100 },
  });
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("dialog", (d) => d.accept());
  await page.route("**/api/v1/**", async (route) => {
    const req = route.request(),
      url = new URL(req.url()),
      p = url.pathname.replace("/api/v1", ""),
      method = req.method(),
      body = req.postDataJSON();
    requests.push({ p, method, body });
    const send = (data, status = 200) => route.fulfill({ status, json: data });
    if (p === "/auth/me") return send(source);
    if (p === "/auth/logout") return route.fulfill({ status: 204 });
    if (p === "/platforms/sources")
      return send({
        items: [
          {
            source: "jwxt",
            name: "杭电教务",
            password_login: true,
            manual_sync_only: true,
            resources: ["schedule", "course"],
          },
        ],
      });
    if (p === "/platforms/connections")
      return send({
        items: [
          {
            id: "fixture-connection",
            user_id: source.id,
            source: "jwxt",
            enabled: true,
            state: "active",
          },
        ],
      });
    if (p.includes("/sync"))
      return send({
        generated: 4,
        skipped_practice: 0,
        retired_modifications: [],
      });
    if (p === "/dws/connections" || p === "/preview/overview")
      return send({ items: [] });
    if (p === "/schedules" && method === "GET") {
      if (delayList) {
        heldList = route;
        return;
      }
      const year = Number(url.searchParams.get("academic_year")),
        term = Number(url.searchParams.get("term")),
        week = Number(url.searchParams.get("teaching_week")),
        offset = Number(url.searchParams.get("offset"));
      const found = items.filter(
        (s) =>
          s.academic_year === year &&
          s.term === term &&
          s.teaching_week === week,
      );
      return send({ items: found.slice(offset, offset + 1000) });
    }
    if (p === "/schedules" && method === "POST") {
      if (failSave)
        return send({ error: { message: "fixture 保存失败" } }, 500);
      const next = event("created", body.title, 28, {
        ...body,
        source: "USER",
      });
      items.push(next);
      return send(next, 201);
    }
    if (p.startsWith("/schedules/")) {
      const id = decodeURIComponent(p.split("/").pop()),
        index = items.findIndex((s) => s.id === id);
      if (index < 0) return send({ error: { message: "不存在" } }, 404);
      if (method === "PATCH") {
        if (failSave)
          return send({ error: { message: "fixture 保存失败" } }, 500);
        const old = items[index];
        if (body.mode) {
          const before = {
            title: old.title,
            start_time: old.start_time,
            end_time: old.end_time,
            details: old.details,
            teaching_week: 8,
          };
          old.mod.history.push({
            mode: body.mode,
            before,
            after: { ...before, ...body },
            effective_week: 8,
            original_id: id,
            at: "2026-11-04T08:00:00Z",
          });
        }
        items[index] = { ...old, ...body };
        return send(items[index]);
      }
      if (method === "DELETE") {
        items.splice(index, 1);
        return route.fulfill({ status: 204 });
      }
      return send(items[index]);
    }
    throw Error("Unexpected API " + method + " " + p);
  });
  const ready = () => page.waitForFunction(() => !busy);
  const card = (id) => page.locator(`[data-schedule-id="${id}"]`);
  const panel = page.locator("#schedulePanel");
  async function setScope(week) {
    await page.locator("#scheduleYear").fill("2026");
    await page.locator("#scheduleTerm").selectOption("1");
    await page.locator("#scheduleWeek").fill(String(week));
    await page.locator("#scheduleWeek").press("Tab");
    await page.waitForFunction(
      (w) =>
        document
          .querySelector("#scheduleStatus")
          .textContent.includes("第 " + w + " 教学周 ·"),
      week,
    );
  }
  try {
    await page.goto("http://127.0.0.1:" + server.address().port + "/admin");
    await page.waitForSelector("#schedulePanel:visible");
    await ready();
    await setScope(8);
    assert.equal(await panel.locator(".schedule-card").count(), 3);
    assert.match(await page.locator("#scheduleParity").textContent(), /双周/);
    assert.equal(await panel.locator("img").count(), 0);
    assert.match(
      await panel.locator(".schedule-day").nth(2).textContent(),
      /数学/,
    );
    assert.match(await card("math").textContent(), /双周安排/);
    assert.match(await card("math").textContent(), /2-16周/);
    checks.push("week_board_and_safe_text_and_parity");
    await card("math")
      .getByRole("button", { name: "标为完成", exact: true })
      .click();
    await ready();
    assert.equal(
      items.find((x) => x.id === "math").event_status,
      "COMPLETED",
      JSON.stringify({
        requests: requests.slice(-4),
        status: await page.locator("#status").textContent(),
        schedule: await page.locator("#scheduleStatus").textContent(),
        errors,
      }),
    );
    assert(
      requests.some(
        (r) => r.method === "PATCH" && r.body.event_status === "COMPLETED",
      ),
    );
    checks.push("independent_completion");
    await card("math")
      .getByRole("button", { name: "编辑", exact: true })
      .click();
    await ready();
    assert.equal(
      await page.locator("#scheduleStart").inputValue(),
      "2026-10-28T14:00",
    );
    await page.locator("#scheduleTitle").fill("临时数学");
    await page.locator("#scheduleForm button[type=submit]").click();
    await ready();
    const temp = requests
      .filter((r) => r.method === "PATCH" && r.body.mode)
      .at(-1).body;
    assert.deepEqual(temp, { mode: "TEMPORARY", title: "临时数学" });
    checks.push("temporary_minimal_patch_and_timezone");
    await card("math")
      .getByRole("button", { name: "编辑", exact: true })
      .click();
    await ready();
    await page.locator("#scheduleMode").selectOption("PERMANENT");
    await page.locator("#scheduleDetails").fill("永久新教室");
    await page.locator("#scheduleForm button[type=submit]").click();
    await ready();
    assert.equal(
      requests.filter((r) => r.method === "PATCH" && r.body.mode).at(-1).body
        .mode,
      "PERMANENT",
    );
    await card("math")
      .getByRole("button", { name: "修改历史", exact: true })
      .click();
    await ready();
    assert.match(
      await page.locator("#scheduleHistoryRows").textContent(),
      /临时修改/,
    );
    assert.match(
      await page.locator("#scheduleHistoryRows").textContent(),
      /永久修改/,
    );
    await page.locator("#scheduleHistoryClose").click();
    checks.push("permanent_and_history");
    await page.locator("#scheduleNew").click();
    await ready();
    await page.locator("#scheduleTitle").fill("新增事项");
    await page.locator("#scheduleStart").fill("2026-10-28T16:00");
    await page.locator("#scheduleEnd").fill("2026-10-28T17:00");
    await page.locator("#scheduleDetails").fill("说明");
    failSave = true;
    await page.locator("#scheduleForm button[type=submit]").click();
    await ready();
    assert.equal(await page.locator("#scheduleForm").isVisible(), true);
    assert.match(
      await page.locator("#scheduleStatus").textContent(),
      /保存失败/,
    );
    failSave = false;
    await page.locator("#scheduleForm button[type=submit]").click();
    await ready();
    assert.equal(await card("created").count(), 1);
    const created = requests
      .filter((r) => r.method === "POST" && r.p === "/schedules")
      .at(-1).body;
    assert.equal(created.teaching_week, 8);
    assert.equal(created.start_time, "2026-10-28T16:00:00+08:00");
    checks.push("create_and_failure_retains_form");
    await card("created")
      .getByRole("button", { name: "删除", exact: true })
      .click();
    await ready();
    assert.equal(await card("created").count(), 0);
    checks.push("delete");
    await page.locator("#scheduleNext").click();
    await ready();
    assert.equal(await panel.locator(".schedule-card").count(), 0);
    assert.equal(await page.locator("#scheduleEmpty").isVisible(), true);
    await page.locator("#schedulePrev").click();
    await ready();
    assert.equal(await panel.locator(".schedule-card").count(), 3);
    checks.push("week_navigation_and_empty_state");
    // Timetable editor, deliberately no source request until all settings validate.
    const editor = page.locator(".period-editor");
    await editor.locator("summary").click();
    const number = editor.getByRole("spinbutton", {
      name: "每天节数",
      exact: true,
    });
    await number.fill("4");
    await editor.getByLabel("首节开始时间", { exact: true }).fill("08:00");
    await editor.getByLabel("每节课时长（分钟）", { exact: true }).fill("45");
    await editor.getByLabel("统一休息时长（分钟）", { exact: true }).fill("10");
    await editor.getByRole("button", { name: "生成 / 重新填充节次" }).click();
    assert.equal(await editor.locator("tbody tr").count(), 4);
    assert.equal(
      await editor.getByLabel("第2节开始", { exact: true }).inputValue(),
      "08:55",
    );
    assert.equal(
      await editor.getByLabel("第4节结束", { exact: true }).inputValue(),
      "11:30",
    );
    assert.equal(await editor.locator(".period-block").count(), 4);
    checks.push("generate_count_duration_rest_and_visual_timeline");
    await editor.getByLabel("第2节课后休息", { exact: true }).fill("90");
    await editor
      .getByLabel("第2节课后休息", { exact: true })
      .dispatchEvent("change");
    await editor
      .locator("tbody tr")
      .nth(1)
      .getByRole("button", { name: "从本节顺延" })
      .click();
    assert.equal(
      await editor.getByLabel("第3节开始", { exact: true }).inputValue(),
      "11:10",
    );
    assert.equal(
      await editor.getByLabel("第4节开始", { exact: true }).inputValue(),
      "12:05",
    );
    checks.push("per_period_break_forward_fill");
    await editor.getByLabel("修改后自动顺延", { exact: true }).check();
    await editor.getByLabel("第1节课时", { exact: true }).fill("50");
    await editor
      .getByLabel("第1节课时", { exact: true })
      .dispatchEvent("change");
    assert.equal(
      await editor.getByLabel("第2节开始", { exact: true }).inputValue(),
      "09:00",
    );
    assert.equal(
      await editor.getByLabel("第3节开始", { exact: true }).inputValue(),
      "11:15",
    );
    checks.push("automatic_forward_fill");
    await editor.getByLabel("首节开始时间", { exact: true }).fill("23:00");
    await editor.getByRole("button", { name: "生成 / 重新填充节次" }).click();
    assert.match(
      await editor.locator(".period-message").textContent(),
      /超出当天/,
    );
    assert.equal(
      await editor.getByLabel("第1节开始", { exact: true }).inputValue(),
      "08:00",
    );
    checks.push("overflow_is_atomic");
    // Year and term fixtures match the client date's initial semester.
    await page.getByLabel("教务学年起始年份", { exact: true }).fill("2026");
    await page
      .getByLabel("教务学年起始年份", { exact: true })
      .dispatchEvent("change");
    await page.getByLabel("教务学期", { exact: true }).selectOption("1");
    await editor
      .getByLabel("第1教学周周一日期", { exact: true })
      .fill("2026-09-07");
    await page.getByRole("button", { name: "获取课表", exact: true }).click();
    await ready();
    const sync = requests.filter((r) => r.p.includes("/sync")).at(-1);
    assert.equal(sync.body.calendar.week_one_monday, "2026-09-07");
    assert.equal(sync.body.calendar.periods.length, 4);
    assert.equal(sync.body.calendar.periods[1].start, "09:00");
    assert.equal(sync.body.calendar.periods[2].start, "11:15");
    assert.equal(await page.locator("#scheduleWeek").inputValue(), "8");
    checks.push("sync_uses_editor_calendar_and_refreshes_panel");
    await page.getByLabel("教务学期", { exact: true }).selectOption("2");
    assert.equal(await editor.locator("tbody tr").count(), 0);
    await page.getByLabel("教务学期", { exact: true }).selectOption("1");
    assert.equal(await editor.locator("tbody tr").count(), 4);
    await page
      .getByRole("button", { name: "刷新学习平台", exact: true })
      .click();
    await ready();
    assert.equal(await page.locator(".period-editor tbody tr").count(), 4);
    checks.push("semester_drafts_and_connection_refresh");
    // Older week response cannot paint over a newer selection.
    delayList = true;
    await page.locator("#scheduleWeek").fill("7");
    await page.locator("#scheduleWeek").press("Tab");
    await page.waitForTimeout(40);
    const oldRoute = heldList;
    delayList = false;
    await setScope(8);
    await oldRoute.fulfill({ json: { items: [] } });
    await page.waitForTimeout(40);
    assert.equal(await panel.locator(".schedule-card").count(), 3);
    checks.push("stale_query_guard");
    if (process.env.SCHEDULE_SCREENSHOT)
      await panel.screenshot({ path: process.env.SCHEDULE_SCREENSHOT });
    // Logout clears records, edit state, history, period drafts, and timer.
    await page.locator("#logout").click();
    await ready();
    assert.equal(await panel.isVisible(), false);
    assert.equal(await panel.locator(".schedule-card").count(), 0);
    assert.equal(await page.evaluate(() => periodDrafts.size), 0);
    checks.push("account_reset");
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ passed: checks.length, checks }, null, 2));
  } finally {
    await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
