"use strict";
// Uses the existing authenticated API and global action guard in admin.html.
let scheduleEpoch = 0,
  scheduleRequest = 0,
  scheduleItems = [],
  scheduleOffset = 0,
  scheduleHasMore = false,
  scheduleLoadedKey = "",
  scheduleEditing = null,
  scheduleTimer = null;
const periodDrafts = new Map();
const scheduleSources = {
  TIMETABLE: "教务课表",
  USER: "个人日程",
  AGENT: "Agent 日程",
};
const scheduleTimes = {
  NOT_STARTED: "未开始",
  IN_PROGRESS: "进行中",
  FINISHED: "已结束",
};
function scheduleMessage(text, error = false) {
  $("scheduleStatus").textContent = text;
  $("scheduleStatus").className = error ? "schedule-error" : "";
}
function scheduleScope() {
  const academic_year = Number($("scheduleYear").value),
    term = Number($("scheduleTerm").value),
    teaching_week = Number($("scheduleWeek").value);
  if (
    !Number.isInteger(academic_year) ||
    academic_year < 2000 ||
    academic_year > 2100 ||
    ![1, 2].includes(term) ||
    !Number.isInteger(teaching_week) ||
    teaching_week < 1 ||
    teaching_week > 60
  )
    throw Error("请输入有效的学年、学期和教学周（1–60）。");
  return { academic_year, term, teaching_week };
}
function scheduleKey() {
  return JSON.stringify(scheduleScope());
}
function scheduleCurrent(owner, epoch) {
  return current?.id === owner && scheduleEpoch === epoch;
}
function scheduleDefaults() {
  const p = new Intl.DateTimeFormat("en-US", {
      timeZone: "Asia/Shanghai",
      year: "numeric",
      month: "numeric",
    }).formatToParts(new Date()),
    y = Number(p.find((x) => x.type === "year").value),
    m = Number(p.find((x) => x.type === "month").value);
  return { year: m >= 8 ? y : y - 1, term: m >= 2 && m <= 7 ? 2 : 1 };
}
function resetSchedulePanel() {
  scheduleEpoch++;
  scheduleRequest++;
  clearInterval(scheduleTimer);
  scheduleTimer = null;
  scheduleItems = [];
  scheduleOffset = 0;
  scheduleHasMore = false;
  scheduleLoadedKey = "";
  scheduleEditing = null;
  periodDrafts.clear();
  $("scheduleBoard").replaceChildren();
  $("scheduleForm").reset();
  $("scheduleForm").hidden = true;
  $("scheduleHistory").hidden = true;
  $("scheduleHistoryRows").replaceChildren();
  $("scheduleMore").hidden = true;
  $("scheduleEmpty").hidden = true;
  const d = scheduleDefaults();
  $("scheduleYear").value = String(d.year);
  $("scheduleTerm").value = String(d.term);
  $("scheduleWeek").value = "1";
  scheduleMessage("");
}
function scheduleWork(work) {
  return action(async () => {
    try {
      await work();
    } catch (e) {
      scheduleMessage(e.message || "日程操作失败", true);
      throw e;
    }
  });
}
function scheduleControls() {
  const week = Number($("scheduleWeek").value);
  $("scheduleParity").textContent =
    Number.isInteger(week) && week >= 1 && week <= 60
      ? "第 " + week + " 教学周 · " + (week % 2 ? "单周" : "双周")
      : "";
  $("scheduleMore").hidden = !scheduleHasMore;
  $("scheduleMore").disabled = busy;
  $("schedulePrev").disabled = busy || Number($("scheduleWeek").value) <= 1;
  $("scheduleNext").disabled = busy || Number($("scheduleWeek").value) >= 60;
}
function scheduleCloseForm() {
  scheduleEditing = null;
  $("scheduleForm").reset();
  $("scheduleForm").hidden = true;
}
function scheduleSelectionChanged() {
  scheduleRequest++;
  scheduleItems = [];
  scheduleLoadedKey = "";
  scheduleHasMore = false;
  $("scheduleBoard").replaceChildren();
  $("scheduleEmpty").hidden = true;
  scheduleCloseForm();
  $("scheduleHistory").hidden = true;
  scheduleControls();
}
async function loadSchedulePanel(more = false) {
  if (!current) return;
  const scope = scheduleScope(),
    key = JSON.stringify(scope),
    owner = current.id,
    epoch = scheduleEpoch,
    request = ++scheduleRequest;
  if (more && key !== scheduleLoadedKey) return;
  if (!more) {
    scheduleItems = [];
    scheduleOffset = 0;
    scheduleHasMore = false;
    scheduleLoadedKey = "";
    $("scheduleBoard").replaceChildren();
    $("scheduleEmpty").hidden = true;
  }
  scheduleMessage("正在读取第 " + scope.teaching_week + " 教学周日程…");
  try {
    const query = new URLSearchParams({
      ...scope,
      limit: "1000",
      offset: String(more ? scheduleOffset : 0),
    });
    const page = await api("/schedules?" + query);
    if (
      !scheduleCurrent(owner, epoch) ||
      request !== scheduleRequest ||
      key !== scheduleKey()
    )
      return;
    if (
      !Array.isArray(page?.items) ||
      page.items.some(
        (s) =>
          s.user_id !== owner ||
          s.academic_year !== scope.academic_year ||
          s.term !== scope.term ||
          s.teaching_week !== scope.teaching_week,
      )
    )
      throw Error("日程响应范围无效，请刷新重试");
    scheduleItems = more ? scheduleItems.concat(page.items) : page.items;
    scheduleOffset = scheduleItems.length;
    scheduleHasMore = page.items.length === 1000;
    scheduleLoadedKey = key;
    renderScheduleBoard();
    scheduleMessage(
      "第 " +
        scope.teaching_week +
        " 教学周 · 已加载 " +
        scheduleItems.length +
        " 条日程" +
        (scheduleHasMore ? "，可继续加载。" : "。"),
    );
    if (!scheduleTimer)
      scheduleTimer = setInterval(() => {
        if (current && scheduleLoadedKey) refreshScheduleTimeLabels();
      }, 30000);
  } catch (e) {
    if (scheduleCurrent(owner, epoch) && request === scheduleRequest)
      scheduleMessage("读取日程失败：" + e.message, true);
    throw e;
  } finally {
    if (scheduleCurrent(owner, epoch)) scheduleControls();
  }
}
function scheduleLocal(value) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "";
  return new Date(date.getTime() + 8 * 3600000).toISOString().slice(0, 16);
}
function scheduleDate(value) {
  return scheduleLocal(value).slice(0, 10);
}
function scheduleTimeText(s) {
  const a = scheduleLocal(s.start_time),
    b = scheduleLocal(s.end_time);
  return (
    a.slice(11) +
    "–" +
    (a.slice(0, 10) === b.slice(0, 10) ? b.slice(11) : b.replace("T", " "))
  );
}
function scheduleStatusNow(s) {
  const now = Date.now();
  return now < new Date(s.start_time).getTime()
    ? "NOT_STARTED"
    : now >= new Date(s.end_time).getTime()
      ? "FINISHED"
      : "IN_PROGRESS";
}
function refreshScheduleTimeLabels() {
  for (const node of $("scheduleBoard").querySelectorAll("[data-time-id]")) {
    const s = scheduleItems.find((x) => x.id === node.dataset.timeId);
    if (s) node.textContent = scheduleTimes[scheduleStatusNow(s)];
  }
}
function renderScheduleBoard() {
  const board = $("scheduleBoard");
  board.replaceChildren();
  const days = [];
  for (let i = 0; i < 7; i++) {
    const day = document.createElement("div");
    day.className = "schedule-day";
    const heading = document.createElement("h3");
    heading.textContent = [
      "周一",
      "周二",
      "周三",
      "周四",
      "周五",
      "周六",
      "周日",
    ][i];
    day.append(heading);
    board.append(day);
    days.push(day);
  }
  const sorted = [...scheduleItems].sort(
    (a, b) =>
      a.start_time.localeCompare(b.start_time) || a.id.localeCompare(b.id),
  );
  for (const s of sorted) {
    const date = scheduleDate(s.start_time),
      day = new Date(date + "T12:00:00+08:00").getUTCDay();
    const index = (day + 6) % 7,
      card = document.createElement("article");
    card.className =
      "schedule-card" + (s.event_status === "COMPLETED" ? " done" : "");
    card.dataset.source = s.source;
    card.dataset.scheduleId = s.id;
    const heading = document.createElement("strong");
    heading.textContent = s.title;
    const time = document.createElement("p");
    time.textContent = date + " · " + scheduleTimeText(s);
    const meta = document.createElement("p");
    meta.className = "schedule-badge";
    const state = document.createElement("span");
    state.dataset.timeId = s.id;
    state.textContent = scheduleTimes[scheduleStatusNow(s)];
    meta.append(
      document.createTextNode((scheduleSources[s.source] || s.source) + " · "),
      state,
      document.createTextNode(
        " · " +
          (s.event_status === "COMPLETED" ? "已完成" : "未完成") +
          (s.mod?.temporary
            ? " · 本周临时调整"
            : s.mod?.permanent
              ? " · 永久调整"
              : ""),
      ),
    );
    const details = document.createElement("p");
    details.textContent = s.details || "";
    card.append(heading, time, meta, details);
    if (s.source === "TIMETABLE" && s.mod?.weeks_raw) {
      const rule = document.createElement("p");
      rule.className = "schedule-badge";
      rule.textContent =
        "教学周：" +
        s.mod.weeks_raw +
        (s.mod.week_pattern === "ODD"
          ? " · 单周安排"
          : s.mod.week_pattern === "EVEN"
            ? " · 双周安排"
            : "");
      card.append(rule);
    }
    const make = (text, fn, danger = false) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = text;
      if (danger) b.className = "danger";
      b.onclick = () => scheduleWork(fn);
      return b;
    };
    card.append(
      make("编辑", async () => openScheduleForm(s)),
      make(
        s.event_status === "COMPLETED" ? "标为未完成" : "标为完成",
        async () => scheduleSetCompletion(s),
      ),
      make("修改历史", async () => showScheduleHistory(s)),
      make("删除", async () => scheduleDelete(s), true),
    );
    days[index].append(card);
  }
  $("scheduleEmpty").hidden = scheduleItems.length !== 0;
  scheduleControls();
}
function openScheduleForm(s = null) {
  const scope = scheduleScope();
  scheduleEditing = s ? { ...s } : null;
  $("scheduleForm").reset();
  $("scheduleForm").hidden = false;
  $("scheduleFormTitle").textContent = s ? "编辑日程" : "新增个人日程";
  $("scheduleFormScope").textContent =
    scope.academic_year +
    " 学年 · 第 " +
    scope.term +
    " 学期 · 第 " +
    scope.teaching_week +
    " 教学周";
  $("scheduleTitle").value = s?.title || "";
  $("scheduleStart").value = s ? scheduleLocal(s.start_time) : "";
  $("scheduleEnd").value = s ? scheduleLocal(s.end_time) : "";
  $("scheduleDetails").value = s?.details || "";
  $("scheduleModeLabel").hidden = !s || s.source !== "TIMETABLE";
  $("scheduleModeHelp").hidden = !s || s.source !== "TIMETABLE";
  $("scheduleMode").value = "TEMPORARY";
  $("scheduleForm").scrollIntoView({ behavior: "smooth", block: "nearest" });
  $("scheduleTitle").focus();
}
async function saveScheduleForm() {
  if (!current) return;
  const scope = scheduleScope(),
    owner = current.id,
    epoch = scheduleEpoch,
    key = scheduleKey(),
    selected = scheduleEditing;
  const start = $("scheduleStart").value,
    end = $("scheduleEnd").value,
    title = $("scheduleTitle").value.trim(),
    details = $("scheduleDetails").value;
  if (
    !title ||
    !start ||
    !end ||
    !Number.isFinite(Date.parse(start + "+08:00")) ||
    !Number.isFinite(Date.parse(end + "+08:00")) ||
    Date.parse(end + "+08:00") <= Date.parse(start + "+08:00")
  )
    throw Error("请填写标题及有效的起止时间，结束时间必须晚于开始时间。");
  let body = {
      ...scope,
      title,
      details,
      start_time: start + ":00+08:00",
      end_time: end + ":00+08:00",
    },
    path = "/schedules",
    method = "POST";
  if (selected) {
    body = {
      mode:
        selected.source === "TIMETABLE" ? $("scheduleMode").value : "PERMANENT",
    };
    if (title !== selected.title) body.title = title;
    if (details !== selected.details) body.details = details;
    if (start !== scheduleLocal(selected.start_time))
      body.start_time = start + ":00+08:00";
    if (end !== scheduleLocal(selected.end_time))
      body.end_time = end + ":00+08:00";
    if (Object.keys(body).length === 1) {
      scheduleCloseForm();
      scheduleMessage("日程没有变化。");
      return;
    }
    if (
      body.mode === "PERMANENT" &&
      selected.source === "TIMETABLE" &&
      !confirm(
        "永久修改会作用于本周及后续教学周的同一课表安排，单周与双周安排分别生效。确认保存？",
      )
    )
      return;
    path += "/" + encodeURIComponent(selected.id);
    method = "PATCH";
  }
  await api(path, method, body);
  if (!scheduleCurrent(owner, epoch) || key !== scheduleKey()) return;
  scheduleCloseForm();
  await loadSchedulePanel();
}
async function scheduleSetCompletion(s) {
  const owner = current?.id,
    epoch = scheduleEpoch,
    key = scheduleKey();
  await api("/schedules/" + encodeURIComponent(s.id), "PATCH", {
    event_status: s.event_status === "COMPLETED" ? "INCOMPLETE" : "COMPLETED",
  });
  if (scheduleCurrent(owner, epoch) && key === scheduleKey())
    await loadSchedulePanel();
}
async function scheduleDelete(s) {
  if (
    !confirm(
      "确认删除“" +
        s.title +
        "”？" +
        (s.source === "TIMETABLE" ? "该周课表安排将隐藏，修改历史保留。" : ""),
    )
  )
    return;
  const owner = current?.id,
    epoch = scheduleEpoch,
    key = scheduleKey();
  await api("/schedules/" + encodeURIComponent(s.id), "DELETE");
  if (scheduleCurrent(owner, epoch) && key === scheduleKey()) {
    if (scheduleEditing?.id === s.id) scheduleCloseForm();
    await loadSchedulePanel();
  }
}
function showScheduleHistory(s) {
  $("scheduleHistory").hidden = false;
  $("scheduleHistoryTitle").textContent = s.title + " · 修改历史";
  const rows = $("scheduleHistoryRows");
  rows.replaceChildren();
  const history = s.mod?.history || [];
  if (!history.length) {
    const p = document.createElement("p");
    p.textContent = "暂无修改记录。";
    rows.append(p);
  }
  for (const h of history) {
    const entry = document.createElement("div"),
      heading = document.createElement("strong"),
      before = document.createElement("p"),
      after = document.createElement("p");
    heading.textContent =
      {
        PERMANENT: "永久修改",
        TEMPORARY: "临时修改",
        SYNC: "教务更新",
        SYNC_REMOVED: "教务取消",
        DELETE: "删除",
      }[h.mode] +
      " · 第 " +
      h.effective_week +
      " 周 · " +
      scheduleLocal(h.at).replace("T", " ");
    const describe = (f) =>
      (f?.title || "") +
      " · " +
      scheduleLocal(f?.start_time).replace("T", " ") +
      " 至 " +
      scheduleLocal(f?.end_time).replace("T", " ") +
      "\n" +
      (f?.details || "");
    before.textContent = "修改前：" + describe(h.before);
    after.textContent = "修改后：" + describe(h.after);
    entry.append(heading, before, after);
    rows.append(entry);
  }
  $("scheduleHistory").scrollIntoView({ behavior: "smooth", block: "nearest" });
}
async function selectScheduleSemester(year, term) {
  $("scheduleYear").value = String(year);
  $("scheduleTerm").value = String(term);
  scheduleSelectionChanged();
  await loadSchedulePanel();
}

function periodMinutes(text) {
  if (!/^\d{2}:\d{2}$/.test(text)) throw Error("请填写有效的课时起止时间。");
  const [h, m] = text.split(":").map(Number);
  if (h > 23 || m > 59) throw Error("时间必须在当天 00:00–23:59 内。");
  return h * 60 + m;
}
function periodClock(value) {
  if (!Number.isInteger(value) || value < 0 || value >= 1440)
    throw Error("顺延后超出当天，请减少节数、课时或休息时长。");
  return (
    String(Math.floor(value / 60)).padStart(2, "0") +
    ":" +
    String(value % 60).padStart(2, "0")
  );
}
function buildPeriods(count, start, duration, rest) {
  if (
    !Number.isInteger(count) ||
    count < 1 ||
    count > 30 ||
    !Number.isInteger(duration) ||
    duration < 1 ||
    duration > 240 ||
    !Number.isInteger(rest) ||
    rest < 0 ||
    rest > 360
  )
    throw Error("节数应为1–30，课时1–240分钟，休息0–360分钟。");
  let cursor = periodMinutes(start);
  return Array.from({ length: count }, () => {
    const row = {
      start: periodClock(cursor),
      end: periodClock(cursor + duration),
      rest,
    };
    cursor += duration + rest;
    return row;
  });
}
function forwardPeriods(rows, index) {
  const result = rows.map((r) => ({ ...r }));
  for (let i = index; i < result.length; i++) {
    const old = rows[i],
      duration = periodMinutes(old.end) - periodMinutes(old.start);
    if (duration < 1) throw Error("每节课结束时间必须晚于开始时间。");
    if (!Number.isInteger(old.rest) || old.rest < 0 || old.rest > 360)
      throw Error("休息时长应为0–360分钟。");
    if (i > index) {
      const start = periodMinutes(result[i - 1].end) + result[i - 1].rest;
      result[i].start = periodClock(start);
      result[i].end = periodClock(start + duration);
    }
  }
  return result;
}
function createPeriodEditor(connection, getSemester) {
  const root = document.createElement("details");
  root.className = "period-editor";
  const summary = document.createElement("summary");
  summary.textContent = "课表时间表编辑器 · 同步课表前配置";
  root.append(summary);
  const content = document.createElement("div");
  root.append(content);
  const intro = document.createElement("p");
  intro.textContent =
    "按该学期真实校历和作息填写。可以统一生成节次，再单独调整午休、晚休或某一节课；配置暂存在当前页面，刷新页面后需重新填写。";
  content.append(intro);
  const label = (text, input) => {
    const l = document.createElement("label");
    l.textContent = text;
    l.append(input);
    return l;
  };
  const input = (type, name, value) => {
    const x = document.createElement("input");
    x.type = type;
    x.setAttribute("aria-label", name);
    x.value = value;
    return x;
  };
  const monday = input("date", "第1教学周周一日期", ""),
    count = input("number", "每天节数", "12"),
    start = input("time", "首节开始时间", "08:00"),
    duration = input("number", "每节课时长（分钟）", "45"),
    rest = input("number", "统一休息时长（分钟）", "10"),
    automatic = input("checkbox", "修改后自动顺延", "");
  count.min = "1";
  count.max = "30";
  duration.min = "1";
  duration.max = "240";
  rest.min = "0";
  rest.max = "360";
  content.append(
    label("第1教学周周一日期", monday),
    label("每天节数", count),
    label("首节开始", start),
    label("每节课（分钟）", duration),
    label("课间休息（分钟）", rest),
    label("修改后自动顺延后续节次", automatic),
  );
  const message = document.createElement("p");
  message.className = "period-message";
  message.setAttribute("role", "status");
  const report = (text, error = false) => {
    message.textContent = text;
    message.className = "period-message" + (error ? " schedule-error" : "");
  };
  const generate = document.createElement("button");
  generate.type = "button";
  generate.textContent = "生成 / 重新填充节次";
  content.append(generate, message);
  const previewScroll = document.createElement("div");
  previewScroll.className = "scroll";
  const preview = document.createElement("div");
  preview.className = "period-preview";
  preview.setAttribute("aria-label", "课时与休息时间轴");
  previewScroll.append(preview);
  content.append(previewScroll);
  const scroll = document.createElement("div");
  scroll.className = "scroll";
  const table = document.createElement("table"),
    thead = document.createElement("thead"),
    head = document.createElement("tr");
  for (const text of [
    "节次",
    "开始",
    "结束",
    "课时（分钟）",
    "课后休息（分钟）",
    "顺延",
  ]) {
    const th = document.createElement("th");
    th.textContent = text;
    head.append(th);
  }
  thead.append(head);
  const tbody = document.createElement("tbody");
  table.append(thead, tbody);
  scroll.append(table);
  content.append(scroll);
  let key = "",
    draft = null;
  const keyFor = () =>
    connection + ":" + getSemester().academic_year + ":" + getSemester().term;
  function remember() {
    draft.monday = monday.value;
    draft.count = count.value;
    draft.start = start.value;
    draft.duration = duration.value;
    draft.rest = rest.value;
    draft.automatic = automatic.checked;
  }
  function redraw() {
    tbody.replaceChildren();
    preview.replaceChildren();
    for (const [i, row] of draft.rows.entries()) {
      const tr = document.createElement("tr"),
        n = document.createElement("td");
      n.textContent = "第 " + (i + 1) + " 节";
      tr.append(n);
      const a = input("time", "第" + (i + 1) + "节开始", row.start),
        b = input("time", "第" + (i + 1) + "节结束", row.end),
        d = input(
          "number",
          "第" + (i + 1) + "节课时",
          String(periodMinutes(row.end) - periodMinutes(row.start)),
        ),
        gap = input("number", "第" + (i + 1) + "节课后休息", String(row.rest));
      d.min = "1";
      d.max = "240";
      gap.min = "0";
      gap.max = "360";
      const change = (kind) => {
        try {
          const rows = draft.rows.map((r) => ({ ...r })),
            next = rows[i];
          if (kind === "duration") {
            const value = Number(d.value);
            if (!Number.isInteger(value) || value < 1 || value > 240)
              throw Error("课时应为1–240分钟。");
            next.end = periodClock(periodMinutes(next.start) + value);
          } else {
            next.start = a.value;
            next.end = b.value;
          }
          next.rest = Number(gap.value);
          if (periodMinutes(next.end) <= periodMinutes(next.start))
            throw Error("结束时间必须晚于开始时间。");
          if (!Number.isInteger(next.rest) || next.rest < 0 || next.rest > 360)
            throw Error("休息应为0–360分钟。");
          draft.rows = automatic.checked ? forwardPeriods(rows, i) : rows;
          remember();
          redraw();
          report(
            automatic.checked
              ? "已顺延后续节次。"
              : "已修改；可点击“从本节顺延”调整后续时间。",
          );
        } catch (e) {
          report(e.message, true);
          redraw();
        }
      };
      a.onchange = () => change("time");
      b.onchange = () => change("time");
      d.onchange = () => change("duration");
      gap.onchange = () => change("rest");
      for (const x of [a, b, d, gap]) {
        const td = document.createElement("td");
        td.append(x);
        tr.append(td);
      }
      const td = document.createElement("td"),
        forward = document.createElement("button");
      forward.type = "button";
      forward.textContent = "从本节顺延";
      forward.onclick = () => {
        try {
          draft.rows = forwardPeriods(draft.rows, i);
          redraw();
          report("已从第 " + (i + 1) + " 节顺延至最后一节。");
        } catch (e) {
          report(e.message, true);
        }
      };
      td.append(forward);
      tr.append(td);
      tbody.append(tr);
      const block = document.createElement("div");
      block.className = "period-block";
      const minutes = periodMinutes(row.end) - periodMinutes(row.start);
      block.style.flex = String(minutes);
      block.textContent = "第" + (i + 1) + "节\n" + row.start + "–" + row.end;
      block.style.whiteSpace = "pre-line";
      preview.append(block);
      const actualGap =
        i < draft.rows.length - 1
          ? periodMinutes(draft.rows[i + 1].start) - periodMinutes(row.end)
          : 0;
      if (actualGap !== 0) {
        const gapBlock = document.createElement("div");
        gapBlock.className = "period-gap";
        gapBlock.style.flex = String(Math.max(1, actualGap));
        gapBlock.textContent =
          actualGap < 0
            ? "重叠 " + -actualGap + "分"
            : "休息 " + actualGap + "分";
        if (actualGap < 0) gapBlock.style.color = "#a51c30";
        preview.append(gapBlock);
      }
    }
    if (!draft.rows.length)
      report(
        "尚未生成节次。上方数值是可编辑的填写建议，请按学校实际作息生成。",
      );
  }
  function switchSemester() {
    key = keyFor();
    if (!periodDrafts.has(key))
      periodDrafts.set(key, {
        monday: "",
        count: "12",
        start: "08:00",
        duration: "45",
        rest: "10",
        automatic: false,
        rows: [],
      });
    draft = periodDrafts.get(key);
    monday.value = draft.monday;
    count.value = draft.count;
    start.value = draft.start;
    duration.value = draft.duration;
    rest.value = draft.rest;
    automatic.checked = draft.automatic;
    report("");
    redraw();
  }
  for (const x of [monday, count, start, duration, rest, automatic])
    x.addEventListener("change", remember);
  generate.onclick = () => {
    try {
      const rows = buildPeriods(
        Number(count.value),
        start.value,
        Number(duration.value),
        Number(rest.value),
      );
      if (
        draft.rows.length &&
        !confirm("重新生成会替换当前学期已编辑的节次，是否继续？")
      )
        return;
      draft.rows = rows;
      remember();
      redraw();
      report("已生成 " + rows.length + " 节。可逐节编辑，并调整午休或晚休。");
    } catch (e) {
      report(e.message, true);
    }
  };
  switchSemester();
  return {
    element: root,
    switchSemester,
    calendar() {
      try {
        if (key !== keyFor()) switchSemester();
        remember();
        const date = new Date(monday.value + "T00:00:00+08:00");
        if (
          !/^\d{4}-\d{2}-\d{2}$/.test(monday.value) ||
          !Number.isFinite(date.getTime()) ||
          new Date(date.getTime() + 8 * 3600000).getUTCDay() !== 1
        )
          throw Error("请选择第1教学周的周一日期。");
        if (!draft.rows.length) throw Error("请先在时间表编辑器中生成节次。");
        const sem = getSemester(),
          year = Number(monday.value.slice(0, 4));
        if (year !== sem.academic_year && year !== sem.academic_year + 1)
          throw Error("首周日期须在所选学年范围内。");
        let last = -1;
        const periods = draft.rows.map((r, i) => {
          const a = periodMinutes(r.start),
            b = periodMinutes(r.end);
          if (a < last || b <= a)
            throw Error(
              "第 " + (i + 1) + " 节与前一节重叠或起止时间无效，请调整或顺延。",
            );
          last = b;
          return { period: i + 1, start: r.start, end: r.end };
        });
        report("时间表校验通过。");
        return { week_one_monday: monday.value, periods };
      } catch (e) {
        root.open = true;
        report(e.message, true);
        throw e;
      }
    },
  };
}

for (const id of ["scheduleYear", "scheduleTerm", "scheduleWeek"])
  document.getElementById(id).addEventListener("change", () => {
    scheduleSelectionChanged();
    loadSchedulePanel().catch(() => {});
  });
document.getElementById("scheduleRefresh").onclick = () =>
  scheduleWork(() => loadSchedulePanel());
document.getElementById("scheduleMore").onclick = () =>
  scheduleWork(() => loadSchedulePanel(true));
for (const [id, delta] of [
  ["schedulePrev", -1],
  ["scheduleNext", 1],
])
  document.getElementById(id).onclick = () =>
    scheduleWork(async () => {
      const value =
        Number(document.getElementById("scheduleWeek").value) + delta;
      if (value < 1 || value > 60) return;
      document.getElementById("scheduleWeek").value = String(value);
      scheduleSelectionChanged();
      await loadSchedulePanel();
    });
document.getElementById("scheduleNew").onclick = () =>
  scheduleWork(async () => openScheduleForm());
document.getElementById("scheduleForm").onsubmit = (e) => {
  e.preventDefault();
  scheduleWork(saveScheduleForm);
};
document.getElementById("scheduleCancel").onclick = scheduleCloseForm;
document.getElementById("scheduleHistoryClose").onclick = () => {
  document.getElementById("scheduleHistory").hidden = true;
};
