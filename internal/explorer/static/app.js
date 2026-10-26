"use strict";

// Read-only explorer: every request is a same-origin GET (or the /events stream) and every
// value from the API is inserted as text, never as HTML.
(function () {
  const PAGE = 20;
  const view = document.getElementById("view");
  const live = document.getElementById("live");

  // state of the "#/" view, kept so a live block can be prepended without refetching.
  const home = { blocks: null, body: null };
  let renderSeq = 0;

  class ApiError extends Error {
    constructor(status, code, message) {
      super(message);
      this.status = status;
      this.code = code;
    }
  }

  // el builds an element; children are strings (text nodes) or nodes. attrs are set as attributes.
  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (k === "class") node.className = v;
      else node.setAttribute(k, v);
    }
    for (const c of children) {
      if (c === null || c === undefined) continue;
      node.append(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return node;
  }

  function link(route, id, text) {
    return el("a", { href: "#/" + route + "/" + encodeURIComponent(id), class: "mono" }, text === undefined ? id : text);
  }

  const short = (hash) => (hash && hash.length > 16 ? hash.slice(0, 8) + "…" + hash.slice(-6) : hash || "");
  const when = (ts) => new Date(ts).toISOString();

  async function get(path) {
    let res;
    try {
      res = await fetch(path, { method: "GET", headers: { Accept: "application/json" } });
    } catch (e) {
      throw new ApiError(0, "network_error", "could not reach the node: " + e.message);
    }
    let body = null;
    try {
      body = await res.json();
    } catch (e) {
      body = null;
    }
    if (!res.ok) {
      throw new ApiError(res.status, (body && body.code) || "http_" + res.status, (body && body.error) || res.statusText || "request failed");
    }
    return body;
  }

  function show(...nodes) {
    view.replaceChildren(...nodes);
  }

  function showError(err) {
    const code = err instanceof ApiError ? err.code : "error";
    const status = err instanceof ApiError && err.status ? " (HTTP " + err.status + ")" : "";
    show(el("div", { class: "error", role: "alert" }, el("strong", {}, code), status, ": " + err.message), el("p", {}, el("a", { href: "#/" }, "back to the latest blocks")));
  }

  function table(headers, rows) {
    return el("table", {}, el("thead", {}, el("tr", {}, ...headers.map((h) => el("th", {}, h)))), el("tbody", {}, ...rows));
  }

  function blockRow(b, fresh) {
    return el(
      "tr",
      { class: fresh ? "fresh" : "" },
      el("td", {}, link("block", String(b.Index))),
      el("td", {}, link("block", String(b.Index), short(b.Hash) || "(genesis)")),
      el("td", {}, when(b.Timestamp)),
      el("td", {}, String((b.Transactions || []).length)),
    );
  }

  function homeTable() {
    return table(["Height", "Hash", "Time", "Txs"], home.blocks.map((b) => blockRow(b, b.fresh)));
  }

  async function renderHome(seq) {
    const health = await get("../health");
    const from = Math.max(0, health.height - (PAGE - 1));
    const page = await get("../blocks?from=" + from + "&limit=" + PAGE);
    if (seq !== renderSeq) return;
    home.blocks = (page.blocks || []).slice().reverse();
    home.body = el("div", {});
    home.body.append(homeTable());
    show(el("h1", {}, "Latest blocks"), el("p", { class: "muted" }, "Tip at height " + health.height + " "), home.body);
  }

  // addLiveBlock puts a block received over SSE on top of the "#/" list.
  function addLiveBlock(b) {
    if (!home.blocks || !view.contains(home.body)) return;
    if (home.blocks.some((x) => x.Index === b.Index)) return;
    home.blocks.forEach((x) => { x.fresh = false; });
    b.fresh = true;
    home.blocks.unshift(b);
    home.blocks.sort((x, y) => y.Index - x.Index);
    home.blocks = home.blocks.slice(0, PAGE);
    home.body.replaceChildren(homeTable());
  }

  function fields(pairs) {
    const dl = el("dl", {});
    for (const [k, v] of pairs) dl.append(el("dt", {}, k), el("dd", { class: "mono" }, v));
    return dl;
  }

  async function renderBlock(seq, ref) {
    const b = await get("../block/" + encodeURIComponent(ref));
    if (seq !== renderSeq) return;
    const txs = b.Transactions || [];
    show(
      el("h1", {}, "Block " + b.Index),
      fields([
        ["Hash", b.Hash || "-"],
        ["Previous", b.Index > 0 ? link("block", String(b.Index - 1), b.PrevHash || "height " + (b.Index - 1)) : "-"],
        ["Time", when(b.Timestamp)],
        ["Merkle root", b.MerkleRoot],
        ["Difficulty", String(b.Difficulty)],
        ["Nonce", String(b.Nonce)],
        ["Validator", b.Validator || "-"],
      ]),
      el("h2", {}, "Transactions (" + txs.length + ")"),
      txs.length ? table(["ID", "Inputs", "Outputs"], txs.map((t) => el("tr", {}, el("td", {}, link("tx", t.ID, short(t.ID))), el("td", {}, String((t.Inputs || []).length)), el("td", {}, String((t.Outputs || []).length))))) : el("p", { class: "muted" }, "No transactions."),
    );
  }

  async function renderTx(seq, id) {
    const res = await get("../tx/" + encodeURIComponent(id));
    if (seq !== renderSeq) return;
    const tx = res.transaction;
    const inputs = tx.Inputs || [];
    const outputs = tx.Outputs || [];
    const nodes = [
      el("h1", {}, "Transaction"),
      fields([
        ["ID", tx.ID],
        ["Block", link("block", String(res.height), "height " + res.height + (res.block_hash ? " · " + short(res.block_hash) : ""))],
        ["Time", when(tx.Timestamp)],
      ]),
      el("h2", {}, "Inputs (" + inputs.length + ")"),
      inputs.length
        ? table(["Spends", "Index"], inputs.map((i) => el("tr", {}, el("td", {}, i.PrevTxID ? link("tx", i.PrevTxID, short(i.PrevTxID)) : "coinbase"), el("td", {}, String(i.OutIndex)))))
        : el("p", { class: "muted" }, "No inputs (coinbase)."),
      el("h2", {}, "Outputs (" + outputs.length + ")"),
      table(["Index", "Address", "Amount"], outputs.map((o, n) => el("tr", {}, el("td", {}, String(n)), el("td", {}, link("address", o.Address)), el("td", {}, String(o.Amount))))),
    ];
    if (tx.Contract) {
      nodes.push(el("h2", {}, "Contract"), fields([["Kind", String(tx.Contract.Kind)], ["Target", String(tx.Contract.Target || "-")], ["Gas limit", String(tx.Contract.GasLimit)]]));
    }
    show(...nodes);
  }

  async function renderAddress(seq, addr) {
    const res = await get("../balance/" + encodeURIComponent(addr));
    if (seq !== renderSeq) return;
    const utxos = res.utxos || [];
    show(
      el("h1", {}, "Address"),
      fields([["Address", res.address], ["Balance", String(res.balance)]]),
      el("h2", {}, "Unspent outputs (" + utxos.length + ")"),
      utxos.length ? table(["Transaction", "Index", "Amount"], utxos.map((u) => el("tr", {}, el("td", {}, link("tx", u.txid, short(u.txid))), el("td", {}, String(u.index)), el("td", {}, String(u.amount))))) : el("p", { class: "muted" }, "No unspent outputs."),
    );
  }

  async function route() {
    const seq = ++renderSeq;
    const parts = location.hash.replace(/^#\/?/, "").split("/");
    let arg = "";
    try {
      arg = decodeURIComponent(parts.slice(1).join("/"));
    } catch (e) {
      showError(new ApiError(0, "bad_route", "malformed address in the URL"));
      return;
    }
    show(el("p", { class: "muted" }, "Loading…"));
    try {
      if (parts[0] === "block" && arg) await renderBlock(seq, arg);
      else if (parts[0] === "tx" && arg) await renderTx(seq, arg);
      else if (parts[0] === "address" && arg) await renderAddress(seq, arg);
      else await renderHome(seq);
    } catch (err) {
      if (seq === renderSeq) showError(err);
    }
  }

  function listen() {
    const source = new EventSource("../events");
    source.onopen = () => {
      live.textContent = "live";
      live.className = "live on";
    };
    source.onerror = () => {
      live.textContent = "reconnecting";
      live.className = "live";
    };
    source.addEventListener("block", (e) => {
      try {
        addLiveBlock(JSON.parse(e.data));
      } catch (err) {
        // a malformed event is ignored; the next navigation reloads from the API
      }
    });
  }

  window.addEventListener("hashchange", route);
  route();
  listen();
})();
