import type { MessageKey } from "./en";

// Vietnamese catalog. `satisfies` forces exact key parity with en.ts;
// i18n.test.ts proves no value is left in English.
export const vi = {
  "app.title": "Design token Margince",
  "app.subtitle":
    "Ledger Green (ADR-0040) — giá trị chuẩn khớp với bản mockup trong spec; test ghim chúng lại.",
  "theme.toDark": "Giao diện tối",
  "theme.toLight": "Giao diện sáng",

  "section.surfaces": "Bề mặt",
  "section.accentAi": "Màu nhấn & AI",
  "section.text": "Chữ",
  "section.status": "Trạng thái",
  "section.typeRamp": "Thang chữ",
  "section.trust": "Thành phần tin cậy (B-EP09.3a)",

  "type.display": "Chữ tiêu đề — Outfit 600",
  "type.body": "Chữ nội dung — DM Sans 400, kiểu chữ đọc mặc định.",
  "type.mono": "Chữ đơn cách — JetBrains Mono, trích đoạn bằng chứng và ID.",

  "trust.accept": "Accept",
  "trust.edit": "Edit",
  "trust.dismiss": "Dismiss",
  "trust.save": "Save",
  "trust.typedByYou": "typed by you",
  "trust.typedByHuman": "typed by a person",
  "trust.typedByPrefix": "typed by",
  "trust.sourceUnknown": "source not recorded",
  "trust.agentTag": "agent: {agent}",
  "trust.connectorTag": "via {connector}",
  "trust.dismissed": "Suggestion dismissed.",
  "trust.stagedProposal": "staged proposal",
  "trust.resolvedValue": "resolved value",
  "trust.editValue": "Edit {description}",
  "trust.evidenceFrom": "Evidence from {source}",

  "history.created": "— đã tạo —",
  "history.oldValue": "Giá trị trước",
  "history.newValue": "Giá trị mới",
  "history.cleared": "— đã xoá —",
  "history.passport": "Passport agent",
  "history.empty": "Chưa ghi nhận thay đổi nào",
  "history.onBehalfOf": "thay mặt {name}",
  "history.fieldEmpty":
    "Được đặt khi tạo và chưa từng đổi — nhật ký kiểm toán không ghi nhận chỉnh sửa nào. Lịch sử trống là trung thực, không phải thiếu sót.",
  "history.filterEmpty": "Không có thay đổi nào khớp bộ lọc này.",
  "history.clearFilter": "Xoá bộ lọc",
  "history.allFields": "Tất cả trường",
  "history.actorAll": "Tất cả",
  "history.actorHuman": "Người",
  "history.actorAgent": "Agent",
  "history.tabChanges": "Thay đổi",
  "history.tabFields": "Lịch sử trường",

  "confidence.high": "high",
  "confidence.med": "medium",
  "confidence.low": "low",

  "autonomy.auto": "auto-execute",
  "autonomy.confirm": "confirm-first",

  "nav.home": "Trang chủ",
  "nav.contacts": "Contact",
  "nav.companies": "Công ty",
  "nav.leads": "Lead",
  "nav.deals": "Pipeline",
  "nav.tasks": "Công việc",
  "nav.inbox": "Phê duyệt",
  "nav.reports": "Báo cáo",
  "nav.ai": "Hỏi Margince",
  "nav.settings": "Cài đặt",
  "nav.design": "Hệ thống thiết kế",
  "nav.automations": "Tự động hoá",
  "nav.group.records": "Dữ liệu",
  "nav.group.work": "Công việc",
  "nav.group.intelligence": "Phân tích",
  "nav.dedupe": "Trùng lặp",
  "nav.products": "Sản phẩm",
  "nav.offerTemplates": "Mẫu báo giá",
  "nav.offers": "Báo giá",
  "nav.share": "Chia sẻ",
  "nav.search": "Kết quả tìm kiếm",

  "shell.railAria": "Điều hướng chính",
  "shell.logoAria": "Margince",
  "shell.search": "Tìm kiếm",
  "shell.searchHint": "Tìm kiếm hoặc chạy lệnh",
  "shell.signOutAria": "Đăng xuất",
  "shell.collapse": "Thu gọn thanh bên",
  "shell.expand": "Mở rộng thanh bên",
  "shell.accountAria": "Tài khoản",
  "shell.views": "Chế độ xem",
  "shell.more": "Thêm",
  "agent.title": "Margince AI",
  "agent.configured": "Configured",
  "agent.exampleActivity": "Enriching 4 new contacts",
  "agent.exampleRouting": "Local + cloud",
  "agent.exampleCost": "€2.41 today",
  "agent.fixture": "Example data",
  "locale.name.en": "English",
  "locale.name.de": "Deutsch",
  "locale.name.vi": "Tiếng Việt",
  "locale.switchLabel": "Ngôn ngữ",

  "screen.pending":
    "Chưa dựng — màn hình này sẽ có cùng ticket xây dựng của nó.",

  "search.title": "Tìm kiếm",
  "search.placeholder": "Tìm người, công ty, deal, hoạt động, lead…",
  "search.empty": "Không có kết quả cho “{q}”.",
  "search.group.person": "Người",
  "search.group.organization": "Tổ chức",
  "search.group.deal": "Deal",
  "search.group.activity": "Hoạt động",
  "search.group.lead": "Lead",
  "search.why": "Vì sao có kết quả này",
  "search.relevance": "độ liên quan {pct}%",
  "search.tier.authoritative": "đã xác minh",
  "search.tier.mirrored": "từ HubSpot",

  "context.title": "Related evidence",
  "context.empty": "Nothing related yet.",

  "palette.aria": "Bảng lệnh",
  "palette.placeholder": "Đi tới, hoặc hỏi bất cứ điều gì…",
  "palette.empty": "Không có kết quả.",
  "palette.askAi": "Hỏi AI: “{query}”",
  "palette.typeScreen": "Màn hình",
  "palette.typeAction": "Hành động",
  "palette.typeRecord": "Bản ghi",
  "palette.seeAll": "Xem tất cả kết quả cho “{query}”",
  "action.newDeal": "New deal",
  "action.readCompany": "Read a company",
  "action.booking": "Booking page",

  "fab.open": "Hỏi về mục này",
  "fab.close": "Đóng",
  "fab.panelAria": "Hỏi về bản ghi này",
  "fab.context": "Hỏi về {context}",
  "fab.scope": "Agent chỉ đọc được những gì bạn thấy được.",
  "fab.inputAria": "Câu hỏi của bạn",
  "fab.placeholder": "Hỏi về những gì bạn đang xem…",
  "fab.send": "Hỏi",

  "explain.open": "Explain this number",
  "explain.title": "How this number is built",
  "explain.rate": "rate {rate} on {date}",

  "brief.nothingSent": "Chưa gửi gì",
  "board.count": "{count} deal",
  "board.weighted": "trọng số {value}",
  "deal.stalled": "đình trệ",
  "deal.singleThreaded": "chỉ một đầu mối",
  "deal.staged": "chờ duyệt",
  "deal.archived": "đã lưu trữ",
  "record.timeline": "Timeline",
  "record.edit": "Sửa",
  "record.save": "Lưu",
  "record.archive": "Lưu trữ",
  "record.disqualify": "Loại bỏ",
  "record.archiveConfirm":
    "Bạn chắc chứ? Thao tác này lưu trữ bản ghi — không có nút hoàn tác.",
  "record.disqualifyConfirm":
    "Bạn chắc chứ? Thao tác này loại bỏ và lưu trữ lead — không có nút hoàn tác.",
  "record.archived": "Đã lưu trữ",
  "record.share": "Chia sẻ",
  "record.moreActions": "Thêm thao tác",
  "record.fullHistory": "Lịch sử đầy đủ",

  "share.title": "Chia sẻ bản ghi này",
  "share.ceiling.pre": "Việc cấp quyền thay đổi ai xem được ",
  "share.ceiling.recordEmphasis": "đúng một bản ghi này",
  "share.ceiling.mid":
    " — không gì khác trong phạm vi của người đó thay đổi. Một lượt chia sẻ bị giới hạn bởi chính quyền truy cập của bạn, ",
  "share.ceiling.noWider": "không rộng hơn",
  "share.ceiling.post": ".",
  "share.unknownRecord": "Đây không phải bản ghi có thể chia sẻ.",
  "share.grantAccess": "Cấp quyền truy cập",
  "share.subject": "Người hoặc nhóm",
  "share.alreadyGranted": "đã được cấp quyền",
  "share.kindPerson": "Người",
  "share.kindTeam": "Nhóm",
  "share.access": "Mức truy cập",
  "share.access.read": "Đọc",
  "share.access.write": "Ghi",
  "share.access.readNote":
    "Mở và đọc được bản ghi này — không sửa hay gửi được.",
  "share.access.writeNote":
    "Mở, sửa và bổ sung được bản ghi này — không đổi được người phụ trách hay việc chia sẻ.",
  "share.expiry": "Thời hạn",
  "share.expiry.none": "Không hết hạn (đến khi thu hồi)",
  "share.expiry.day": "Hết hạn sau 24 giờ",
  "share.expiry.week": "Hết hạn sau 7 ngày",
  "share.expiry.month": "Hết hạn sau 30 ngày",
  "share.reason": "Lý do",
  "share.grant": "Cấp quyền truy cập",
  "share.whoHasAccess": "Ai có quyền truy cập",
  "share.grantedBy": "cấp bởi",
  "share.revoke": "Thu hồi",
  "share.revokeConfirm":
    "Thu hồi quyền này? Người được cấp sẽ mất quyền truy cập bản ghi ở yêu cầu kế tiếp — không có nút hoàn tác.",
  "share.approvalRequired":
    "Lượt chia sẻ này cần được phê duyệt trước khi có hiệu lực — nó đã được xếp vào hộp phê duyệt, chưa áp dụng.",
  "share.empty": "Chưa có quyền nào được cấp thủ công trên bản ghi này.",
  "share.teamMembers.one": "Nhóm · {count} thành viên",
  "share.teamMembers.other": "Nhóm · {count} thành viên",
  "share.rosterLoading": "Đang tải danh sách người và nhóm…",
  "share.rosterErrorUsers":
    "Không tải được danh sách người — các nhóm hiển thị bên dưới.",
  "share.rosterErrorTeams":
    "Không tải được danh sách nhóm — những người dùng hiển thị bên dưới.",
  "share.rosterErrorBoth": "Không tải được danh sách người hay nhóm.",
  "share.rosterEmpty": "Không tìm thấy người hay nhóm nào có thể chia sẻ.",

  "edit.versionSkew":
    "Bản ghi đã thay đổi kể từ khi bạn mở — tải lại rồi thử lại.",

  "merge.person": "Gộp contact",
  "merge.org": "Gộp công ty",
  "merge.searchPlaceholder": "Tìm kiếm…",
  "merge.pickTarget": "Chọn bản ghi được giữ lại",
  "merge.confirm": "Gộp {source} vào {target}? {source} sẽ được lưu trữ.",
  "merge.submit": "Gộp",

  "tab.overview": "Tổng quan",
  "tab.relationships": "Người & công ty",
  "tab.partner": "Đối tác",
  "tab.rollup": "Tổng hợp",
  "tab.history": "Lịch sử",

  "rollup.weightedPipeline": "Pipeline theo trọng số",
  "rollup.closedWon": "Đã thắng (quý hiện tại)",
  "rollup.activity30d": "Hoạt động (30 ngày)",
  "rollup.accounts": "Tài khoản đã gộp",
  "rollup.excluded": "Đã loại trừ {count} tài khoản bạn không xem được",
  "rollup.fxUnavailable":
    "Thiếu một tỷ giá quy đổi — không tính được số tổng hợp.",
  "rollup.computedAt": "Tính lúc {when}",

  "nav.partners": "Đối tác",
  "partner.setup": "Đặt làm đối tác",
  "partner.edit": "Sửa đối tác",
  "partner.none": "Chưa phải đối tác",
  "partner.organization": "Tổ chức",
  "partner.role": "Vai trò đối tác",
  "partner.certStatus": "Trạng thái chứng nhận",
  "partner.marginTier": "Bậc chiết khấu",
  "partner.stage": "Giai đoạn quan hệ",
  "partner.nextStep": "Bước tiếp theo",
  "partner.nextStepDue": "Hạn bước tiếp theo",
  "partner.servedSegments": "Segment phục vụ",
  "partner.servedSegmentsHint": "phân tách bằng dấu phẩy",
  "partner.role.hosting": "Hosting",
  "partner.role.consulting": "Tư vấn",
  "partner.role.strategic": "Chiến lược",
  "partner.cert.applied": "Đã nộp hồ sơ",
  "partner.cert.certified": "Đã chứng nhận",
  "partner.cert.suspended": "Đã tạm ngưng",
  "partner.marginTier.tier1": "Bậc 1 (15%)",
  "partner.marginTier.tier2": "Bậc 2 (20%)",
  "partner.marginTier.tier3": "Bậc 3 (25%)",
  "partner.stage.research": "Nghiên cứu",
  "partner.stage.identified": "Đã xác định",
  "partner.stage.contacted": "Đã liên hệ",
  "partner.stage.inConversation": "Đang trao đổi",
  "partner.stage.fitConfirmed": "Đã xác nhận phù hợp",
  "partner.stage.agreementPending": "Chờ ký thoả thuận",
  "partner.stage.active": "Đang hoạt động",
  "partner.stage.activeReferring": "Đang hoạt động — có giới thiệu",
  "partner.stage.dormant": "Tạm lắng",
  "partner.stage.noFit": "Không phù hợp",

  "rel.add": "Thêm quan hệ",
  "rel.kind": "Loại",
  "rel.role": "Vai trò",
  "rel.startedAt": "Bắt đầu",
  "rel.endedAt": "Kết thúc",
  "rel.current": "hiện tại",
  "rel.remove": "Gỡ",
  "rel.removeConfirm":
    "Bạn chắc chứ? Thao tác này gỡ quan hệ — không có nút hoàn tác.",
  "rel.empty": "Chưa có quan hệ nào",
  "rel.counterparty": "Liên kết với",
  "rel.dates": "Thời gian",
  "rel.pickCounterparty": "Chọn bên còn lại",
  "rel.addConfirm": "Thêm liên kết {kind} tới {target}.",
  "rel.kind.employment": "Việc làm",
  "rel.kind.dealStakeholder": "Bên liên quan deal",
  "rel.kind.projectStakeholder": "Bên liên quan dự án",
  "rel.kind.partnerOf": "Đối tác của",
  "rel.kind.referredBy": "Được giới thiệu bởi",
  "rel.kind.coSellWith": "Bán chung với",

  "common.error": "Không tải được màn hình này.",
  "common.retry": "Thử lại",
  "common.empty": "Chưa có gì ở đây.",
  "common.saving": "Đang lưu…",

  "list.search": "Tìm kiếm",
  "list.sort": "Sắp xếp",
  "list.showArchived": "Hiện mục lưu trữ",
  "list.loadMore": "Tải thêm",
  "list.sortNewest": "Mới nhất",
  "list.sortScore": "Điểm",
  "list.overlayReadOnly": "Sắp xếp và bộ lọc đọc qua HubSpot — hãy mở bên đó",
  "overlay.unavailable":
    "Không dùng được khi đang đọc từ HubSpot — hãy mở bên HubSpot",
  "overlay.chipLabel": "Đang đọc từ HubSpot",
  "overlay.chipAria":
    "Bản cài đặt này đọc bản ghi từ bản sao HubSpot thay vì các bảng gốc. Hãy mở Cài đặt → Overlay để quản lý kết nối.",
  "overlay.refused":
    "Không dùng được khi đang đọc từ HubSpot — bản sao không phục vụ được lượt ghi này.",
  "overlay.filterUnsupported":
    "Bộ lọc hay cách sắp xếp này không dùng được khi đang đọc từ HubSpot — hãy bỏ đi rồi thử lại.",
  "overlay.emptyOwnerHint":
    "Danh sách trống ở đây thường có nghĩa là email HubSpot của người phụ trách không khớp người dùng nào trong workspace, chứ không phải portal HubSpot trống.",
  "overlay.partialWriteBack":
    "Chỉ những trường HubSpot chấp nhận mới được ghi ngược lại — mọi thứ khác ở đây, kể cả trường tuỳ chỉnh và người phụ trách, hoàn toàn không được áp dụng; giá trị hiện tại bên HubSpot vẫn giữ nguyên.",

  "overlay.title": "Bản sao HubSpot",
  "overlay.sub":
    "Kết nối CRM đang dùng của workspace để bản ghi được đọc từ bản sao của CRM đó thay vì các bảng gốc.",
  "overlay.loading": "Đang tải kết nối tới CRM đang dùng…",
  "overlay.notConfigured":
    "Chế độ Overlay chưa được cấu hình trên bản triển khai này.",
  "overlay.loadFailed": "Không tải được kết nối tới CRM đang dùng.",
  "overlay.empty":
    "Chưa kết nối CRM đang dùng nào. Hãy kết nối HubSpot để đọc bản ghi từ bản sao HubSpot.",
  "overlay.adminOnly": "Bạn không có quyền kết nối HubSpot.",
  "overlay.region": "Khu vực",
  "overlay.regionEu1": "EU",
  "overlay.regionUs": "Hoa Kỳ",
  "overlay.token": "Token private app",
  "overlay.tokenHint":
    "Được niêm phong vào kho khoá; không hiển thị lại lần nào nữa.",
  "overlay.connect": "Kết nối HubSpot",
  "overlay.reconnect": "Kết nối lại",
  "overlay.connectConfirmTitle": "Kết nối HubSpot cho cả workspace?",
  "overlay.reconnectConfirmTitle": "Kết nối lại HubSpot cho cả workspace?",
  "overlay.connectConfirmBody":
    "Thao tác này chuyển ngay phần đọc dữ liệu của mọi người dùng sang bản sao HubSpot, và bản ghi trở thành chỉ đọc ở bất cứ đâu bản sao không phục vụ được lượt ghi. Việc này ảnh hưởng toàn bộ bản cài đặt, không chỉ phiên của riêng bạn.",
  "overlay.statusActive": "Đã kết nối",
  "overlay.statusRevoked": "Đã thu hồi",
  "overlay.statusError": "Lỗi đồng bộ",
  "overlay.connectedAt": "Kết nối {at}",
  "overlay.syncTitle": "Đồng bộ bản sao",
  "overlay.syncLoading": "Đang tải trạng thái đồng bộ…",
  "overlay.syncLoadFailed": "Không tải được trạng thái đồng bộ.",
  "overlay.syncEmpty": "Chưa đồng bộ được gì.",
  "overlay.syncStateFresh": "Mới nhất",
  "overlay.syncStatePending": "Chờ đồng bộ",
  "overlay.syncStateStale": "Đã cũ",
  "overlay.backfillDone": "Đã nạp xong dữ liệu cũ",
  "overlay.backfillPending": "Đang nạp dữ liệu cũ",
  "overlay.lastSynced": "Đồng bộ lần cuối {at}",
  "overlay.neverSynced": "Chưa đồng bộ lần nào",
  "overlay.budgetTitle": "Hạn mức API",
  "overlay.budgetLoading": "Đang tải cửa sổ hạn mức…",
  "overlay.budgetLoadFailed": "Không tải được cửa sổ hạn mức.",
  "overlay.budgetHeadroom": "Còn dư: {headroom}",
  "overlay.budgetSources":
    "Force-fresh {forceFresh} · Poller {poller} · Capture {capture}",
  "overlay.budgetSearch": "API tìm kiếm: {consumed} / {limit} mỗi giây",
  "overlay.bandOk": "Ổn định",
  "overlay.bandWarn": "Sắp chạm giới hạn",
  "overlay.bandShed": "Đang giảm tải",
  "overlay.reconcile": "Đồng bộ ngay",
  "overlay.reconcileQueued":
    "Đã xếp hàng lượt quét — worker sẽ nhận ở lần kiểm tra kế tiếp (khoảng 2 phút một lần).",
  "overlay.disconnect": "Ngắt kết nối",
  "overlay.disconnectTitle": "Ngắt kết nối HubSpot?",
  "overlay.disconnectBody":
    "Thao tác này xoá sạch dữ liệu đã sao và chuyển workspace về dùng bản ghi gốc. Nhật ký kiểm toán vẫn được giữ.",

  "overlay.userMap.title": "Ánh xạ người dùng của bản sao",
  "overlay.userMap.sub":
    "Mỗi người dùng workspace tương ứng với người dùng {principal} nào. Ánh xạ này quyết định toàn bộ những gì họ thấy trong bản sao.",
  "overlay.userMap.cost":
    "Người dùng không có ánh xạ sẽ không thấy bản ghi đã sao nào — danh sách của họ trả về trống.",
  "overlay.userMap.loading": "Đang tải ánh xạ người dùng…",
  "overlay.userMap.loadFailed": "Không tải được ánh xạ người dùng.",
  "overlay.userMap.adminOnly": "Bạn không có quyền xem ai đã được ánh xạ.",
  "overlay.userMap.notOverlay":
    "Workspace này đọc từ các bảng gốc, nên không có gì để ánh xạ.",
  "overlay.userMap.notConfigured":
    "Chế độ Overlay chưa được cấu hình trên bản triển khai này.",
  "overlay.userMap.empty": "Workspace này không có người dùng nào để ánh xạ.",
  "overlay.userMap.view": "Nhóm theo",
  "overlay.userMap.viewByUser": "Theo người dùng",
  "overlay.userMap.viewByOwner": "Theo người dùng {principal}",
  "overlay.userMap.principal.hubspot": "HubSpot",
  "overlay.userMap.principal.generic": "CRM đã kết nối",
  "overlay.userMap.you": "Bạn",
  "overlay.userMap.matchEmail": "Khớp theo email",
  "overlay.userMap.matchManual": "Đặt thủ công",
  "overlay.userMap.map": "Gán ánh xạ…",
  "overlay.userMap.change": "Đổi ánh xạ…",
  "overlay.userMap.unmap": "Bỏ ánh xạ",
  "overlay.userMap.cancel": "Huỷ",
  "overlay.userMap.pickerLabel": "Tìm người dùng {principal}",
  "overlay.userMap.truncated":
    "Danh bạ {principal} dài hơn danh sách này — người bạn không tìm thấy ở đây có thể nằm ngoài phần đã tải.",
  "overlay.userMap.directoryFailed":
    "Không đọc được danh bạ {principal}, nên hiện chưa chọn được ai.",
  "overlay.userMap.notMapped": "Chưa ánh xạ",
  "overlay.userMap.chip.noEmailMatch": "Không khớp email",
  "overlay.userMap.chip.ambiguousEmail": "Email trùng nhiều người",
  "overlay.userMap.chip.blockedByAdmin": "Quản trị viên đã bỏ ánh xạ",
  "overlay.userMap.chip.notYetSynced": "Chưa đồng bộ",
  "overlay.userMap.chip.directoryUnavailable": "Chưa rõ lý do",
  "overlay.userMap.reason.noEmailMatch":
    "Không người dùng {principal} nào có địa chỉ email này.",
  "overlay.userMap.reason.ambiguousEmail":
    "Hai người dùng {principal} trở lên dùng chung địa chỉ email này, nên không thể khớp tự động một cách an toàn.",
  "overlay.userMap.reason.blockedByAdmin":
    "Một quản trị viên đã bỏ ánh xạ người dùng này, và việc khớp tự động sẽ không ánh xạ lại.",
  "overlay.userMap.reason.notYetSynced":
    "Danh bạ {principal} chưa liệt kê người dùng này.",
  "overlay.userMap.reason.directoryUnavailable":
    "Không đọc được trọn danh bạ {principal}, nên không suy ra được lý do.",
  "overlay.userMap.staleChip": "Không còn trong danh bạ {principal}",
  "overlay.userMap.staleNote":
    "Ánh xạ thủ công này không mở thêm quyền xem nào. Hệ thống chỉ báo lại, không bao giờ tự gỡ — quyết định vẫn thuộc về bạn.",
  "overlay.userMap.unmapTitle": "Bỏ ánh xạ người dùng này?",
  "overlay.userMap.unmapSelfTitle": "Bỏ ánh xạ của chính bạn?",
  "overlay.userMap.unmapBody":
    "{user} sẽ không còn thấy bản ghi đã sao nào cho đến khi được ánh xạ lại.",
  "overlay.userMap.unmapSelfBody":
    "Bạn sẽ không còn thấy bản ghi đã sao nào cho đến khi được ánh xạ lại. Tab này vẫn mở được, nên bạn có thể hoàn tác tại đây.",
  "overlay.userMap.sharedSeat": "Dùng chung — {count} người dùng",
  "overlay.userMap.ownerEmpty":
    "Chưa ai được ánh xạ tới người dùng {principal}.",
  "overlay.userMap.unmappedCountOne":
    "1 người dùng chưa được ánh xạ và không hiện ở đây — hãy chuyển sang Theo người dùng để xử lý.",
  "overlay.userMap.unmappedCount":
    "{count} người dùng chưa được ánh xạ và không hiện ở đây — hãy chuyển sang Theo người dùng để xử lý.",
  "overlay.userMap.partialView":
    "Cách nhóm và số đếm này chỉ tính những người dùng đã tải. Hãy tải thêm để xem phần còn lại.",

  "people.name": "Tên",
  "people.email": "Email",
  "people.capturedBy": "Ghi nhận bởi",
  "person.consent": "Chấp thuận",
  "consent.grant": "Cấp chấp thuận",
  "consent.withdraw": "Rút lại",
  "consent.doubleOptIn": "Phát hành xác nhận kép",
  "consent.doiIssued": "Token dùng một lần (chỉ hiện một lần):",
  "consent.doiExpires": "Hết hạn",
  "consent.noRecord": "chưa ghi nhận",
  "consent.noPurposes": "Workspace này chưa theo dõi mục đích chấp thuận nào.",
  "consent.defaultDeny":
    "Việc gửi ra mặc định bị từ chối theo từng mục đích: một lượt gửi bị chặn trừ khi có sự chấp thuận đang hiệu lực và có bằng chứng cho đúng mục đích đó. Chấp thuận cho một mục đích không bao giờ cho phép một mục đích khác.",
  "consent.proofLog": "Nhật ký bằng chứng",
  "consent.proofEmpty":
    "Chưa ghi nhận quyết định chấp thuận nào cho mục đích này. Nhật ký trống là trung thực, không phải thiếu sót.",
  "consent.sourceUnknown": "không ghi nhận nguồn",
  "consent.tokenLabel": "Token xác nhận",
  "consent.tokenHint":
    "Mục đích này cần xác nhận kép: hãy dán token dùng một lần để sự chấp thuận có hiệu lực.",
  "consent.actorHuman": "Người",
  "consent.actorAgent": "Agent",
  "consent.actorSystem": "Hệ thống",
  "consent.actorConnector": "Connector",
  "consent.actorUnknown": "không ghi nhận tác nhân",
  "consent.purposesUnavailable":
    "Không tải được danh mục mục đích chấp thuận, nên hiện chưa thể cho biết mục đích nào cần xác nhận kép.",

  "org.name": "Công ty",
  "org.industry": "Ngành",
  "org.size": "Quy mô",
  "org.classification": "Loại",
  // Offered only where there is no partner programme yet: the tab that holds
  // the form appears once one exists, so this is how the first one is made.
  // Where the account stands with us, and what it is to us — the two
  // questions the retired classification answered with one value.
  "org.lifecycle": "Giai đoạn",
  "org.relationshipTypes": "Quan hệ với chúng ta",
  "org.lifecycle.unknown": "Chưa đánh giá",
  "org.lifecycle.target": "Mục tiêu",
  "org.lifecycle.prospect": "Tiềm năng",
  "org.lifecycle.opportunity": "Cơ hội",
  "org.lifecycle.customer": "Khách hàng",
  "org.lifecycle.former_customer": "Khách hàng cũ",
  "org.lifecycle.disqualified": "Đã loại",
  "org.relType.customer": "Khách hàng",
  "org.relType.partner": "Đối tác",
  "org.relType.supplier": "Nhà cung cấp",
  "org.relType.investor": "Nhà đầu tư",
  "org.relType.portfolio_company": "Công ty trong danh mục",
  "org.relType.competitor": "Đối thủ",
  "org.relType.other": "Khác",
  // Why a stored fact contradicts its own field. The fact is still shown
  // with its evidence — a reader can tell, and hiding it would be worse.
  "co.factSuspect.phoneShapedLocation": "Trông giống số điện thoại",
  "co.factSuspect.notAPhone": "Không giống số điện thoại",
  "co.factSuspect.notAYear": "Không giống một năm",
  "co.factSuspect.notAnEmail": "Không giống địa chỉ email",
  "co.factSuspect.notASize": "Không giống quy mô nhân sự",
  // The three readings the overview leads with, and what performing a
  // suggestion means. "Whose move" is the question the 0-100 score was
  // mistaken for.
  "co.strip.title": "Tình hình tài khoản này",
  "co.strip.account": "Giai đoạn",
  "co.strip.engagement": "Đến lượt ai",
  "co.strip.commercial": "Việc đang mở",
  "co.strip.engagement.never_contacted": "Chưa từng liên hệ",
  "co.strip.engagement.active": "Đang trao đổi",
  "co.strip.engagement.waiting_on_them": "Đang chờ họ",
  "co.strip.engagement.waiting_on_us": "Đang chờ bên mình",
  "co.strip.engagement.dormant": "Đã lặng đi",
  "co.strip.lastBoth": "Họ viết {inbound} · bên mình viết {outbound}",
  "co.strip.never": "chưa bao giờ",
  "co.strip.openDeals": "{count} đang mở",
  "co.strip.stalled": "{count} đình trệ",
  "co.suggest.act.draftReply": "Soạn thư trả lời",
  "co.suggest.act.openDeal": "Mở deal",
  "co.suggest.act.addTask": "Thêm bước tiếp theo",
  // A conversation shown as one event says what it IS before what it
  // says: the reader is scanning for an event, not a sentence.
  "timeline.group.thread": "{count} tin nhắn",
  "timeline.group.threadOne": "1 tin nhắn",
  "timeline.group.bulk": "gửi tới {count} người",
  "timeline.group.bulkOne": "gửi tới 1 người",
  "timeline.group.expand": "Mở",
  "timeline.group.collapse": "Đóng",
  "timeline.group.openThread": "Xem toàn bộ luồng",
  "timeline.group.mayContinue": "có thể còn tiếp phía trước",
  "tab.people": "Người",
  "tab.timeline": "Lịch sử",
  // The brief under the questions it answers, and what kind of claim each
  // sentence makes — a judgment must not read as a stored fact.
  "co.brief.section.snapshot": "Họ là ai",
  "co.brief.section.fit": "Vì sao quan trọng với bên mình",
  "co.brief.section.health": "Tình hình ra sao",
  "co.brief.section.activity": "Đã có gì xảy ra",
  "co.brief.section.next_step": "Nên làm gì",
  "co.brief.nature.fact": "Dữ kiện",
  "co.brief.nature.assessment": "Nhận định",
  "co.brief.nature.recommendation": "Gợi ý",
  "co.health.title": "Tình hình ra sao",
  "co.health.sinceInbound": "Họ viết lần cuối cách đây {days} ngày",
  "co.health.replyBalance": "{percent}% lượt trao đổi đến từ họ",
  "co.health.activeContacts": "{count} người ở đây đã từng tương tác",
  "co.health.openCommitments": "{count} cam kết đang mở",
  "co.health.singleThreaded": "Cả tài khoản này chỉ dựa vào một contact",
  "org.partnerSetUp": "Thiết lập chương trình đối tác",
  // The classification values a reader sees. The column stores the enum;
  // rendering the enum itself ("prospect") told a German reader nothing and
  // an English one only slightly more.
  "org.class.prospect": "Tiềm năng",
  "org.class.customer": "Khách hàng",
  "org.class.agency": "Công ty dịch vụ",
  "org.class.reseller": "Nhà bán lại",
  "org.class.tech_vendor": "Nhà cung cấp công nghệ",
  "org.class.platform": "Nền tảng",
  "org.class.partner": "Đối tác",
  "org.class.competitor": "Đối thủ",
  "org.class.other": "Khác",
  "org.class.explain":
    "Công ty này quan hệ thế nào với bạn — không phải giai đoạn trong một deal.",
  "signal.kind.stalled_deal": "Deal đình trệ",
  "signal.kind.champion_left": "Người ủng hộ đã rời đi",
  "signal.kind.reengagement": "Đáng nối lại liên hệ",
  "signal.kind.buying_intent": "Ý định mua",
  "signal.kind.risk": "Rủi ro",
  "signal.kind.other": "Khác",
  "signal.kind.contract_ended": "Hợp đồng sắp hết hạn",
  "signal.kind.new_opportunity": "Cơ hội mới",
  "signal.kind.commitment_made": "Đã có lời hứa",
  "signal.kind.ghosted_thread": "Không hồi đáp",
  "co.strip.signal": "Đáng lưu ý",
  "co.routeIn.open": "Đường tiếp cận",
  "co.routeIn.title": "Ai bên mình trao đổi với {name}",
  "co.routeIn.none": "Chưa ai bên mình viết cho họ.",
  "co.routeIn.partial":
    "Không có đường tiếp cận nào trong số kết nối mà trang này đọc được — một số bị giữ lại hoặc bỏ sót.",
  "co.routeIn.mayBeMore":
    "Một số kết nối bị giữ lại hoặc bỏ sót, nên có thể còn nữa.",
  "co.routeIn.band.strong": "liên hệ đều đặn",
  "co.routeIn.band.some": "có liên hệ ít nhiều",
  "co.routeIn.band.faint": "gần như không liên hệ",
  "co.routeIn.band.unknown": "có ghi nhận liên hệ, chưa rõ nhịp",
  "record.profile": "Hồ sơ",
  "record.business": "Kinh doanh",
  "co.pulse.strongestLead": "Đường tiếp cận",
  "co.pulse.strengthTail.one": "— contact duy nhất ở đây",
  "co.pulse.strengthTail.other": "— trong {count} contact ở đây",
  "co.pulse.noStrength": "Chưa ghi nhận tương tác nào",
  // Two timestamps, never folded into one "last touch": which side wrote
  // last is the question. Mailed a fortnight ago with no reply, and wrote
  // to us this morning, are the same date and opposite situations.
  "co.pulse.lastInbound": "Họ viết {when}",
  "co.pulse.lastOutbound": "Bên mình viết {when}",
  "co.pulse.noInbound": "Họ chưa bao giờ viết",
  "co.pulse.noOutbound": "Bên mình chưa bao giờ viết",
  "co.pulse.neverTouched": "Chưa từng liên hệ",
  "co.pulse.owner": "Người phụ trách",
  "co.owner.notInRoster":
    "Người phụ trách hiện tại (không còn trong danh sách người dùng)",
  "co.pulse.unowned": "Chưa giao",
  "co.since.first": "Bạn đang mở tài khoản này lần đầu.",
  "co.partial":
    "Một phần trang này không tải được, nên có thể chưa hiển thị hết mọi thứ của tài khoản này.",
  "evidence.explain": 'Where "{value}" came from',
  "evidence.fullHistory": "Full history",
  "co.section.unavailable": "Không tải được — đây có thể chưa phải toàn cảnh",
  "co.section.restricted": "Đã ẩn — vai trò của bạn không đọc được phần này",
  "co.next.title": "Bước tiếp theo",
  "co.next.empty": "Không có công việc nào đang mở trên tài khoản này.",
  "co.next.overdue": "Quá hạn",
  "co.next.due": "Hạn {when}",
  "co.next.undated": "Chưa có ngày",
  "co.next.done": "Đánh dấu xong",
  "co.next.assignee": "Người xử lý",
  "co.people.title": "Người",
  "co.people.empty": "Chưa có contact nào gắn với tài khoản này.",
  "co.people.singleThread":
    "Chỉ một contact — tài khoản này chỉ có một đầu mối",
  "co.people.consentGranted": "Được phép liên hệ",
  "co.people.consentWithdrawn": "Đã rút lại",
  "co.people.consentUnknown": "Chưa ghi nhận chấp thuận",
  "co.brief.title": "Trước khi bạn nói chuyện với họ",
  "co.brief.unavailable":
    "Không tải được phần nhận định về tài khoản, nên đây chưa phải toàn cảnh.",
  "co.brief.empty": "Tài khoản này còn quá ít dữ liệu để rút ra được điều gì.",
  "co.brief.rewrite": "Viết lại",
  "co.brief.rewriting": "Đang viết…",
  "co.brief.by.model": "Do Margince viết",
  "co.brief.by.deterministic": "Tổng hợp từ dữ liệu của bạn",
  "co.brief.generatedAt": "tính đến {when}",
  "co.brief.cite.deal": "deal",
  "co.brief.cite.activity": "hoạt động",
  "co.brief.cite.person": "contact",
  "co.brief.cite.organization": "tài khoản",
  "co.brief.cite.fact": "dữ kiện",
  // Several sources of one kind that have no screen to open collapse into one
  // counted chip, rather than a run of identical labels.
  "co.brief.cite.deal.many": "{count} deal",
  "co.brief.cite.activity.many": "{count} hoạt động",
  "co.brief.cite.person.many": "{count} contact",
  "co.brief.cite.organization.many": "{count} tài khoản",
  "co.brief.cite.fact.many": "{count} dữ kiện",
  "approval.kind.advance_deal": "Chuyển deal tiến lên",
  "approval.kind.close_date_correction": "Sửa ngày chốt",
  "approval.kind.deal_follow_up": "Thêm việc theo dõi cho deal",
  "approval.kind.promote_lead": "Chuyển đổi một lead",
  "approval.kind.archive_record": "Lưu trữ một bản ghi",
  "approval.kind.merge_records": "Gộp hai bản ghi",
  "approval.kind.share_record": "Chia sẻ một bản ghi",
  "approval.kind.update_record": "Cập nhật một bản ghi",
  "approval.kind.create_record": "Tạo một bản ghi",
  "approval.kind.send_email": "Gửi một email",
  "approval.kind.book_meeting": "Đặt một lịch họp",
  "approval.kind.send_offer": "Gửi một báo giá",
  "approval.kind.coldstart": "Điền thông tin công ty mới",
  "approval.kind.enrich": "Bổ sung thông tin từ web",
  "approval.kind.deepread": "Đọc website công ty",
  "approval.kind.linkedin_match": "Đối chiếu LinkedIn",
  "approval.kind.site_lead": "Thêm người tìm thấy trên website",
  "approval.kind.capture_counterparty": "Thêm người từ email của bạn",
  "approval.kind.org_name_promotion": "Đổi tên một công ty",
  "approval.kind.lifecycle_change": "Giai đoạn công ty",
  "approval.kind.fx_rate_proposal": "Làm mới tỷ giá",
  "approval.kind.ai_model_rate_proposal": "Làm mới giá mô hình",
  "co.assistant.title": "Hỏi về tài khoản này",
  "co.assistant.aiTag": "Có AI hỗ trợ",
  "co.decisions.open": "Xem {count} mục đang chờ",
  "co.decisions.title": "Quyết định đang chờ",
  "co.decisions.group": "{count} × {kind}",
  "co.decisions.empty": "Ở đây không có gì đang chờ quyết định.",
  "co.ask.title": "Hỏi Margince",
  "co.ask.q.whats_open": "Ở đây đang mở những gì?",
  "co.ask.q.meeting_prep": "Chuẩn bị cho tôi một cuộc họp",
  "co.ask.q.whats_changed": "Gần đây có gì thay đổi?",
  "co.ask.nothing": "Không có gì bạn xem được ở đây trả lời được câu đó.",
  "co.ask.failed": "Không trả lời được câu hỏi đó — hãy thử lại.",
  "co.suggest.title": "Đáng làm tiếp theo",
  "co.suggest.kind.no_reply": "Chưa có hồi đáp",
  "co.suggest.kind.stalled_deal": "Deal đình trệ",
  "co.suggest.kind.no_next_step": "Chưa có gì lên lịch",
  "co.suggest.kind.lifecycle_conflict": "Bản ghi mâu thuẫn",
  "co.suggest.more": "Còn {count} mục không hiện ở đây.",
  "co.suggest.dismiss": "Để sau",
  "co.suggest.dismissFailed":
    "Không bỏ qua được mục này — mục đó vẫn hiển thị với bạn",
  "co.deals.title": "Deal",
  "co.deals.empty": "Không có deal nào đang mở trên tài khoản này.",
  "co.deals.wonLifetime": "Đã thắng đến nay",
  "co.deals.lostCount": "{count} đã thua",
  "co.deals.noStage": "Chưa có giai đoạn",
  "co.connections.title": "Kết nối",
  "co.connections.empty": "Chưa có gì liên kết với tài khoản này.",
  "co.connections.ourSide": "Bên mình",
  "co.connections.theirSide": "Bên tài khoản này",
  "co.connections.expand": "Xem lớn hơn",
  "co.connections.collapse": "Đóng",
  "co.connections.introPath": "Đường tiếp cận",
  "co.connections.more": "Còn {count} mục không hiện ở đây.",
  "co.connections.withheld": "Bị ẩn với bạn: {groups}",
  "co.connections.rel.employment": "làm việc ở đây",
  "co.connections.rel.has_deal": "deal đang mở",
  "co.connections.rel.deal_stakeholder": "bên liên quan của một deal",
  "co.connections.rel.parent": "công ty mẹ",
  "co.connections.rel.child": "công ty con",
  "co.connections.rel.partner_of.counterparty": "đối tác của tài khoản này",
  "co.connections.rel.partner_of.owner": "tài khoản này là đối tác của họ",
  "co.connections.rel.referred_by.counterparty": "đã giới thiệu tài khoản này",
  "co.connections.rel.referred_by.owner": "được tài khoản này giới thiệu",
  "co.connections.rel.co_sell_with": "bán chung",
  "co.connections.rel.owns": "phụ trách tài khoản này",
  "co.connections.rel.in_contact_with": "đang liên hệ",
  "co.connections.noSignal": "chưa có tín hiệu",
  "linkedinImport.title": "LinkedIn connections",
  "linkedinImport.sub":
    "Import your own export to see who your team already knows",
  "linkedinImport.explainer":
    "LinkedIn gives you a Connections.csv under Settings → Data privacy → Get a copy of your data. Uploading it here shows who on your team already knows someone at an account. The connections do NOT become contacts: they never appear in search, lists or contact pages, and nobody can write to or email them.",
  "linkedinImport.profileLabel": "Your LinkedIn profile URL",
  "linkedinImport.profilePlaceholder": "https://www.linkedin.com/in/…",
  "linkedinImport.saveProfile": "Save profile",
  "linkedinImport.connectedNote":
    "Connected. Imported connections are attributed to this profile, so the CRM can say which colleague knows someone rather than that \u201cthe company\u201d does.",
  "linkedinImport.notConnectedNote":
    "Not connected yet. Adding your profile URL attributes any connections you import to you by name.",
  "linkedinImport.whichFile":
    "The file you want is Connections.csv \u2014 the export archive holds a dozen others.",
  "linkedinImport.choose": "Choose Connections.csv",
  "linkedinImport.noMatchesYet":
    "No matches yet, which is normal on a new workspace: your connections are matched against contacts the CRM knows, and those arrive as your mail is read. This runs again every hour, so matches appear as the CRM fills up.",
  "linkedinImport.working": "Reading your export…",
  "linkedinImport.imported": "Connections imported",
  "linkedinImport.confirmed": "Matched to a contact",
  "linkedinImport.suggested": "Awaiting your confirmation",

  // The review queue and the reach table (ADR-0078 §2.1b).
  "linkedinReach.title": "Where your network reaches",
  "linkedinReach.sub":
    "Accounts on file where you already know somebody, most connections first.",
  "linkedinReach.empty":
    "None of your connections work at an account on file yet.",
  "linkedinReach.allUnresolved":
    "All {unresolved} of your connections work somewhere that is not an account on file yet.",
  "linkedinReach.account": "Account",
  "linkedinReach.connections": "You know",
  "linkedinReach.onFile": "Already contacts",
  "linkedinReach.onFileOf": "{onFile} of {total}",
  "linkedinReach.footnote":
    "Showing {shown} of {total} accounts. {unresolved} connections work somewhere that is not an account on file yet.",
  "linkedinImport.skipped": "Rows skipped (no usable name)",
  "co.connections.group.contacts": "contact",
  "co.connections.group.deals": "deal",
  "co.connections.group.intro_path": "lời giới thiệu qua người quen",
  "co.connections.group.our_side": "ai bên mình có kết nối",
  "co.signals.title": "Tín hiệu",
  "co.signals.empty": "Không có tín hiệu nào đang mở trên tài khoản này.",
  "co.chronology.label": "Hiện gì trên timeline",
  "co.chronology.activities": "Hoạt động",
  "co.chronology.changes": "Thay đổi",
  "co.chronology.all": "Tất cả",
  "co.chronology.changesEmpty":
    "Chưa trường nào của bản ghi này thay đổi kể từ khi được tạo.",
  "co.chronology.allEmpty": "Chưa có gì xảy ra trên tài khoản này.",
  "co.chronology.truncated":
    "Các mục cũ hơn không hiện ở đây — cả hai loại đều nhiều hơn mức màn hình này sắp xếp nổi. Hãy chọn Hoạt động hoặc Thay đổi để đọc ngược xa hơn.",
  "co.chronology.truncatedActivities":
    "Tài khoản này có nhiều hoạt động hơn sức chứa ở đây. Chỉ những hoạt động mới nhất được liệt kê.",
  "timeline.sent": "Đã gửi",
  "timeline.received": "Đã nhận",
  "timeline.textMore": "Đọc",
  "timeline.textLess": "Thu gọn",
  "co.profileField.display_name": "Tên công ty",
  "co.profileField.offer_summary": "Họ bán gì",
  "co.profileField.icp": "Họ bán cho ai",
  "co.profileField.buying_center": "Ai quyết định bên họ",
  "co.profileField.value_proposition": "Họ hứa hẹn điều gì",
  "co.profileField.usp": "Họ khác biệt ở đâu",
  "co.profileField.customer_pains": "Vấn đề họ giải quyết",
  "co.profileField.desired_outcomes": "Kết quả họ hứa hẹn",
  "co.profileField.buying_intents": "Điều gì thúc đẩy quyết định mua",
  "co.profileField.common_objections": "Phản đối họ thường gặp",
  "co.profileField.sales_motion": "Họ bán như thế nào",
  "co.profileField.legal_name": "Tên pháp lý đã đăng ký",
  "co.profileField.registered_address": "Địa chỉ đăng ký",
  "co.profileField.register_vat": "Số đăng ký / mã số thuế",
  "co.profileField.industry": "Ngành",
  "co.profileField.history": "Lịch sử",
  "co.profile.title": "Hồ sơ công ty",
  "co.reach.window": "Tình trạng liên hệ trong 90 ngày qua",
  "co.reach.answered": "Đã hồi đáp",
  "co.reach.silent": "Chưa hồi đáp",
  "co.reach.untried": "Chưa tiếp cận",
  "co.role.set": "Đặt vai trò",
  "co.role.setOn": "{name} giữ vai trò gì trong deal này?",
  "co.role.explain":
    "Người ủng hộ sẽ lên tiếng cho bạn khi bạn không có mặt. Người quyết định ngân sách là người ký. Chỉ khi nêu tên được cả hai thì một danh sách contact mới thành bức tranh của quyết định.",
  "co.role.onDeal": "Trong deal nào",
  "co.role.role": "Vai trò",
  "co.role.champion": "người ủng hộ",
  "co.role.economic_buyer": "người quyết định ngân sách",
  "co.role.blocker": "người cản trở",
  "co.role.influencer": "người tác động",
  "co.role.user": "người dùng cuối",
  "co.people.missing":
    "Deal đang mở chưa nêu tên {roles} nào — hãy đặt vai trò đó cho đúng contact.",
  "co.people.untriedHint": "{count} người ở đây chưa từng được tiếp cận.",
  "co.people.untriedHintOne": "Một người ở đây chưa từng được tiếp cận.",
  "co.evidence.title": "Nguồn của thông tin này",
  "co.relationships.title": "Người và công ty đã liên kết",
  "co.tools.title": "Dữ liệu & công cụ",
  "co.prep.title": "Trước khi bạn nói chuyện với họ",
  "co.prep.sparse":
    "Tài khoản này gần như chưa có lịch sử, nên chưa có gì để chuẩn bị.",
  "co.prep.withheld":
    "Một số phần của tài khoản này bị ẩn với bạn, nên nhận định này chưa đầy đủ.",
  "co.read.lastTouch": "Lần liên hệ gần nhất cách đây {days} ngày.",
  "co.read.lastTouchOne": "Lần liên hệ gần nhất là hôm qua.",
  "co.read.neverTouched":
    "Chưa ai bên mình từng liên hệ với bất kỳ ai ở tài khoản này.",
  "co.read.newActivityOne": "Một mục mới kể từ lần bạn xem gần nhất.",
  "co.read.newActivityMany": "{count} mục mới kể từ lần bạn xem gần nhất.",
  "co.read.dealMovedOne":
    "Một deal đã đổi giai đoạn kể từ lần bạn xem gần nhất.",
  "co.read.dealMovedMany":
    "{count} deal đã đổi giai đoạn kể từ lần bạn xem gần nhất.",
  "co.read.unansweredOne":
    "Bạn đã viết cho một contact ở đây trong {days} ngày qua mà chưa có hồi đáp.",
  "co.read.unansweredMany":
    "Bạn đã viết cho {count} contact ở đây trong {days} ngày qua mà chưa ai hồi đáp.",
  "co.read.noContacts": "Bạn chưa quen ai ở tài khoản này.",
  "co.read.singleThread":
    "Chỉ {name} có email, cuộc gọi hay cuộc họp được ghi nhận trong {days} ngày qua.",
  "co.read.oneContact":
    "{name} là đường tiếp cận duy nhất của bạn vào tài khoản này.",
  "co.read.noChampion.one": "Deal đang mở chưa nêu tên người ủng hộ nào.",
  "co.read.noChampion.other": "Không deal đang mở nào nêu tên người ủng hộ.",
  "co.read.stalled": "{name} đã đình trệ.",
  "co.read.noOpenDeal":
    "Không có deal nào đang mở, và ở đây cũng chưa thắng được gì.",
  "co.read.noOpenDealCustomer":
    "Hiện không có deal nào đang mở, dù tài khoản này đã từng mua.",
  "co.read.overdueOne": "Quá hạn: {subject}",
  "co.read.overdueMany": "{count} cam kết ở đây đã quá hạn.",
  "co.read.noNextStep":
    "Tài khoản này chưa có bước tiếp theo nào được lên lịch.",
  "co.factField.founded_year": "Thành lập",
  "co.factField.employee_range": "Nhân sự",
  "co.factField.phone": "Điện thoại",
  "co.factField.contact_email": "Email liên hệ",
  "co.factField.location": "Địa điểm",
  "co.factField.service": "Dịch vụ",
  "co.factField.product": "Sản phẩm",
  "co.factField.capability": "Năng lực",
  "co.factField.served_industry": "Phục vụ",
  "co.factField.company_size": "Quy mô",
  "co.factField.geography": "Khu vực",
  "co.factField.language": "Ngôn ngữ",
  "co.factField.certification": "Chứng nhận",
  "co.factField.partner": "Đối tác",
  "co.factField.named_customer": "Khách hàng",
  "co.factField.technology": "Công nghệ",
  "co.factField.quantified_outcome": "Kết quả",
  "co.facts.showAll": "Hiện tất cả {count}",
  "co.facts.showLess": "Hiện bớt",
  "co.facts.title": "Dữ kiện nhanh",
  "co.tags.lists": "Danh sách",
  "co.tags.tags": "Tag",
  "co.tags.noLists": "Không thuộc danh sách nào.",
  "co.tags.noTags": "Chưa gắn tag nào.",
  "co.deal.new": "Deal mới",
  "co.tags.apply": "Thêm tag",
  "co.tags.pick": "Tên tag",
  "co.lists.add": "Thêm vào danh sách",
  "co.lists.pick": "Tên danh sách",
  "co.tags.title": "Danh sách & tag",
  "co.tags.empty": "Không thuộc danh sách nào và chưa gắn tag nào.",
  "co.timeline.filterKind": "Lọc theo loại",
  "co.timeline.filterAll": "Mọi loại",
  "co.timeline.filterPerson": "Lọc theo người",
  "co.timeline.allPeople": "Mọi người",
  "co.timeline.via": "Liên quan",
  "co.timeline.empty": "Chưa ghi nhận gì trên tài khoản này.",
  "co.overlayFallback":
    "Tài khoản này được phục vụ từ hệ thống ghi nhận đã kết nối, nên màn hình công ty không được dựng ở đây. Hãy mở bên hệ thống đó để xem toàn cảnh.",
  "org.firmographics": "Thông tin doanh nghiệp",
  "org.domains": "Tên miền",
  "org.firmographicsEmpty":
    "Chưa đọc được gì — các trường hồ sơ có căn cứ sẽ hiện ở đây khi một lần đọc website xác nhận chúng.",
  "org.facts": "Dữ kiện đọc từ website",
  "org.factCategory.company": "Công ty",
  "org.factCategory.offering": "Sản phẩm dịch vụ",
  "org.factCategory.market": "Thị trường",
  "org.factCategory.signal": "Tín hiệu",

  "lead.score": "Điểm",
  "lead.status": "Trạng thái",
  "lead.segregated":
    "lead nằm tách khỏi mạng lưới contact cho đến khi được chuyển đổi",
  "lead.promote": "Chuyển thành contact",
  "lead.promoteIneligible": "cần có email và trạng thái đang mở",
  "lead.filterStatus": "Trạng thái",
  "lead.statusNew": "Mới",
  "lead.statusWorking": "Đang xử lý",
  "lead.statusPromoted": "Đã chuyển đổi",
  "lead.statusDisqualified": "Đã loại",
  "lead.disqualified": "Đã loại",
  "lead.status.new": "Mới",
  "lead.status.working": "Đang xử lý",
  "lead.setStatus": "Trạng thái",
  "lead.explainScore": "Giải thích điểm này",
  "lead.scoreOverridden": "Người ghi đè: {reason}",
  "lead.machineScore": "Điểm máy chấm là {score}",
  "lead.overrideScore": "Ghi đè điểm",
  "lead.clearOverride": "Xoá ghi đè",
  "lead.overrideReason": "Lý do",
  "lead.machineComputed": "Điểm do máy tính toán",
  "lead.owner": "Phụ trách: {owner}",
  "lead.ownerYou": "bạn",
  "lead.overriddenBadge": "đã ghi đè",
  "lead.unassigned": "Chưa giao",
  "lead.assignToMe": "Giao cho tôi",
  "lead.saveOverride": "Lưu ghi đè",
  "lead.overrideScoreValue": "Điểm",
  "lead.promoteDialog": "Chuyển thành contact",
  "lead.trigger": "Căn cứ chuyển đổi",
  "lead.trigger.inboundReply": "Có phản hồi đến",
  "lead.trigger.meetingBooked": "Đã đặt lịch họp",
  "lead.trigger.meetingHeld": "Đã họp",
  "lead.trigger.humanQualify": "Người xác nhận đủ điều kiện",
  "lead.evidenceNote": "Ghi chú bằng chứng (không bắt buộc)",
  "lead.promoteConfirm": "Chuyển đổi",

  "deals.viewBoard": "Bảng",
  "deals.viewTable": "Bảng biểu",
  "deals.amount": "Giá trị",
  "deals.stage": "Giai đoạn",
  "deals.close": "Dự kiến chốt",
  "deals.confirmAdvance": "Chuyển sang {stage}?",
  "deals.confirmTerminal":
    "Thao tác này chốt deal ở trạng thái {status}. Hãy xác nhận trước — chưa có gì xảy ra cho đến khi bạn xác nhận.",
  "deals.lostReason": "Lý do thua",
  "deals.confirm": "Xác nhận",
  "deals.cancel": "Huỷ",
  "deals.advanced": "Đã chuyển sang {stage}",
  "deal.pendingApprovals": "Đang chờ bạn xác nhận",
  "deal.stakeholders": "Bên liên quan",
  "deal.edit": "Sửa deal",
  "deal.ownerKeep": "Giữ người phụ trách hiện tại",
  "deal.ownerMe": "Giao cho tôi",
  "deal.ownerUnassign": "Bỏ giao",
  "deal.partnerOrg": "Tổ chức đối tác",
  "deal.forecastCategory": "Nhóm dự báo",
  "deal.waitUntil": "Chờ đến",
  "deal.fxBase": "Gốc {value} · tỷ giá {rate} tính đến {date}",
  "deal.archive": "Lưu trữ deal",
  "deal.archiveConfirm":
    "Lưu trữ sẽ đưa deal này ra khỏi pipeline đang hoạt động. Không thể hoàn tác từ giao diện.",
  "deal.reopen": "Mở lại",
  "deal.reopenPick": "Chuyển deal này về một giai đoạn đang mở",
  "deal.reopenConfirm": "Mở lại",
  "deal.fcCommit": "Cam kết",
  "deal.fcBestCase": "Khả quan nhất",
  "deal.fcPipeline": "Pipeline",
  "deal.fcOmitted": "Loại trừ",

  "deals.pipeline": "Pipeline",
  "deals.filterStalled": "Chỉ deal đình trệ",
  "deals.filterOwnerMe": "Deal của tôi",
  "deals.filterPartnerSourced": "Do đối tác mang về",
  "deals.filterStageAll": "Mọi giai đoạn",
  "deals.filterOrgAll": "Mọi công ty",
  "deals.filterStalledAll": "Mọi deal",
  "deals.filterOwnerAll": "Mọi người phụ trách",
  "deals.filterPartnerAll": "Mọi nguồn",
  "deals.sortNewest": "Mới nhất",
  "deals.sortClose": "Ngày chốt",
  "deals.sortAmount": "Lớn nhất",

  "deal.offers": "Báo giá",
  "deal.newOffer": "Báo giá mới",
  "deal.offerNumber": "Số báo giá",
  "deal.offerRevision": "Bản",
  "deal.offersEmpty": "Chưa có báo giá",

  "offer.revision": "Bản {revision}",
  "offer.backToDeal": "Quay lại deal",
  "offer.totals": "Tổng",
  "offer.net": "Trước thuế",
  "offer.tax": "Thuế",
  "offer.gross": "Sau thuế",
  "offer.edit": "Sửa phần đầu",
  "offer.currency": "Tiền tệ",
  "offer.buyerOrg": "Tổ chức mua",
  "offer.buyerOrgConfirm": "Tổ chức mua: {name}",
  "offer.template": "Mẫu",
  "offer.validUntil": "Hiệu lực đến",
  "offer.introText": "Lời mở đầu",
  "offer.termsText": "Điều khoản",
  "offer.lines": "Các dòng hàng",
  "offer.addLine": "Thêm dòng",
  "offer.position": "STT",
  "offer.description": "Mô tả",
  "offer.unit": "Đơn vị",
  "offer.quantity": "Số lượng",
  "offer.unitPrice": "Đơn giá",
  "offer.discountPct": "Chiết khấu %",
  "offer.taxRate": "Thuế %",
  "offer.lineTotal": "Thành tiền",
  "offer.unpriced": "chưa có giá — không tính vào tổng",
  "offer.removeLine": "Gỡ",
  "offer.pickProduct": "Chọn sản phẩm",
  "offer.pickProductConfirm": "Sản phẩm: {name}",
  "offer.send": "Gửi",
  "offer.sendConfirm": "Gửi báo giá này cho bên mua?",
  "offer.sendBody": "Báo giá sẽ thành chỉ đọc cho đến khi bên mua phản hồi.",
  "offer.accept": "Chấp nhận",
  "offer.acceptConfirm": "Đánh dấu báo giá này là đã chấp nhận?",
  "offer.acceptBody":
    "Giá trị và tiền tệ của deal sẽ được cập nhật cho khớp báo giá này.",
  "offer.reject": "Từ chối",
  "offer.rejectConfirm": "Đánh dấu báo giá này là đã từ chối?",
  "offer.rejectReason": "Lý do (không bắt buộc)",
  "offer.regenerate": "Tạo lại bản mới",
  "offer.aiDisclosureTitle": "Công bố có AI hỗ trợ",
  "offer.diffAdded": "Đã thêm {count} dòng",
  "offer.diffRemoved": "Đã gỡ {count} dòng",
  "offer.diffChanged": "Đã đổi {count} dòng",
  "offer.renderPdf": "Tạo PDF",
  "offer.viewPdf": "Xem PDF",
  "offer.pdfUnavailable": "Bản triển khai này không tạo được PDF.",

  "inbox.sub":
    "everything staged, waiting on your call — nothing runs without it",
  "inbox.expires": "expires {at}",
  "inbox.viaTool": "via {verb}",
  "inbox.approveEdited": "Approve edited",
  "inbox.reject": "Reject",
  "inbox.tab.pending": "Pending",
  "inbox.tab.decided": "Decided",
  "inbox.rejectReason": "Reason",
  "inbox.rejectReasonHint": "Shared with the person this was staged for.",
  "inbox.tokenTitle": "Approval token",
  "inbox.tokenOnce": "Copy it now — you'll only see this token once.",
  "inbox.copy": "Copy",
  "inbox.copied": "Copied",
  "inbox.tokenDone": "Done",
  "inbox.dismiss": "Dismiss",
  "inbox.versionSkew":
    "This record changed since it was staged — re-stage it before deciding.",
  "inbox.reRead": "Re-read",
  "inbox.alreadyDecided": "Already decided — nothing left to do here.",
  "inbox.expired": "Expired",
  "inbox.expiresIn": "expires in {countdown}",
  "inbox.detail": "Approval detail",
  "inbox.status.approved": "Approved",
  "inbox.status.rejected": "Rejected",
  "inbox.status.expired": "Expired",

  "home.brief": "Tóm tắt buổi sáng",
  "home.sub": "xếp hạng từ tín hiệu trực tiếp — việc chờ xác nhận lên trước",
  "home.staged": "Đang chờ bạn",
  "home.stalled": "Deal đình trệ",
  "home.queue": "Việc hôm nay",
  "home.asOf": "tính đến {at}",
  "home.refresh": "Làm mới tóm tắt",
  "home.refreshing": "Đang xếp hạng…",
  "home.generate": "Tạo bản tóm tắt đầu tiên",
  "home.noneTitle": "Chưa có bản tóm tắt",
  "home.noneBody":
    "Tóm tắt buổi sáng xếp hạng những deal đáng dành giờ đầu tiên — khả năng thắng, doanh thu, thời điểm, đà tiến và độ thân thiết, mỗi yếu tố kèm bằng chứng của nó. Hãy tạo lần chạy đầu tiên khi bạn đã có deal đang mở.",
  "home.honestShort":
    "Chỉ {count} deal vượt ngưỡng — danh sách không bao giờ được độn thêm.",
  "home.overflow":
    "{shown} trong {count} deal đạt ngưỡng — phần đầu ngắn gọn và trung thực.",
  "home.quietRun":
    "Sáng nay không có gì vượt ngưỡng. Không bịa ra việc gấp — hãy tận hưởng sự yên tĩnh.",
  "home.act": "Xong",
  "home.dismiss": "Bỏ qua",
  "home.actedState": "đã xử lý",
  "home.dismissedState": "đã bỏ qua",
  "home.why": "Vì sao xếp hạng này",
  "home.evidence": "{count} dòng bằng chứng",
  "home.evidenceOne": "1 dòng bằng chứng",
  "home.score": "điểm {pct}%",
  "home.openDeal": "Mở deal",
  "home.factorWinnability": "Khả năng thắng",
  "home.factorRevenue": "Doanh thu",
  "home.factorTiming": "Thời điểm",
  "home.factorMomentum": "Đà tiến",
  "home.factorWarmth": "Độ thân thiết",

  "home.digest": "Thu thập qua đêm",
  "home.digestFor": "tổng hợp ngày {date}",
  "home.digestSynced": "Email đã đồng bộ",
  "home.digestPeople": "Người đã tạo",
  "home.digestOrgs": "Công ty đã tạo",
  "home.digestApprovals": "Phê duyệt đang chờ",
  "home.digestDedupe": "Trùng lặp cần rà",
  "home.digestClassify":
    "Phân loại qua đêm: {commitments} cam kết · {meetings} cuộc họp · {noise} nhiễu",

  "enrich.title": "Read from the website",
  "enrich.sub":
    "evidence-or-omit — fills only empty fields, and only after your approval",
  "enrich.cta": "Read now",
  "enrich.reading": "Reading…",
  "enrich.staged":
    "Staged — nothing written yet. Accept it in your inbox; only empty fields fill.",
  "enrich.toInbox": "Open inbox",
  "enrich.from": "read from {url}",

  "deepread.title": "Read the full site",
  "deepread.sub":
    "Reads up to 12 pages of the company's website. Findings are staged for your review — nothing is written until you accept.",
  "deepread.cta": "Read full site",
  "deepread.starting": "Starting…",
  "deepread.unavailable": "Site reading is not configured on this server.",
  "deepread.statusQueued": "Queued",
  "deepread.statusDeferred": "Waiting for AI budget",
  "deepread.statusRunning": "Reading…",
  "deepread.statusDone": "Done",
  "deepread.statusPartial": "Stopped early",
  "deepread.statusFailed": "Failed",
  "deepread.statusCancelled": "Cancelled",
  "deepread.resumesAt": "Resumes automatically {when}.",
  "deepread.pagesSoFar.one": "{count} page read so far",
  "deepread.pagesSoFar.other": "{count} pages read so far",
  "deepread.stoppedEarly": "Stopped early: {reason}",
  "deepread.stopBudget": "model budget",
  "deepread.stopPageCap": "page cap",
  "deepread.stopByteCap": "byte cap",
  "deepread.stopDeadline": "deadline",
  "deepread.factCount.one": "{count} evidenced fact staged",
  "deepread.factCount.other": "{count} evidenced facts staged",
  "deepread.pagesRead": "Pages read",
  "deepread.skippedPages": "Pages skipped",
  "deepread.skipRobots": "robots.txt",
  "deepread.skipOffDomain": "off domain",
  "deepread.skipPageCap": "page cap",
  "deepread.skipByteCap": "byte cap",
  "deepread.skipUnreadable": "unreadable",
  "deepread.proposals": "{count} proposals waiting for your review",
  "deepread.proposalsOne": "1 proposal waiting for your review",
  "deepread.kindHome": "Home",
  "deepread.kindImpressum": "Impressum",
  "deepread.kindAbout": "About",
  "deepread.kindTeam": "Team",
  "deepread.kindServices": "Services",
  "deepread.kindProducts": "Products",
  "deepread.kindContact": "Contact",
  "deepread.kindOther": "Other",

  "create.cancel": "Huỷ",
  "create.multiselect.required": "Bắt buộc — chọn ít nhất một.",
  "create.save": "Tạo",
  "create.saving": "Đang tạo…",
  "create.contact": "Contact mới",
  "create.company": "Công ty mới",
  "create.lead": "Lead mới",
  "create.deal": "Deal mới",
  "create.fullName": "Họ và tên",
  "create.firstName": "Tên",
  "create.lastName": "Họ",
  "create.personTitle": "Chức danh",
  "create.email": "Email",
  "create.phone": "Điện thoại",
  "create.linkedin": "LinkedIn",
  "create.linkedinUrl": "Đường dẫn LinkedIn",
  "create.candidateOrgKey": "Khoá tổ chức dự kiến",
  "create.displayName": "Tên công ty",
  "create.legalName": "Tên pháp lý",
  "create.industry": "Ngành",
  "create.sizeBand": "Quy mô công ty",
  "create.domain": "Tên miền chính",
  "create.companyName": "Công ty",
  "create.dealName": "Tên deal",
  "create.amount": "Giá trị",
  "create.currency": "Tiền tệ",
  "create.stage": "Giai đoạn",
  "create.organization": "Công ty",
  "create.expectedClose": "Dự kiến chốt",

  "field.addEmail": "Add email",
  "field.addPhone": "Add phone",
  "field.addDomain": "Add domain",
  "field.domain": "Domain",
  "field.emailType": "Type",
  "field.emailWork": "Work",
  "field.emailPersonal": "Personal",
  "field.emailOther": "Other",
  "field.phoneType": "Type",
  "field.phoneWork": "Work",
  "field.phoneMobile": "Mobile",
  "field.phoneHome": "Home",
  "field.phoneOther": "Other",
  "field.primary": "Primary",
  "field.removeRow": "Remove",
  "field.yes": "Yes",
  "field.no": "No",

  "dedupe.viewExisting": "View existing record",

  "log.title": "Ghi nhận hoạt động",
  "log.sub": "một ghi chú hay công việc, thẳng lên timeline này",
  "log.kind": "Loại",
  "log.kindNote": "Ghi chú",
  "log.kindTask": "Công việc",
  "log.subject": "Tiêu đề",
  "log.body": "Nội dung",
  "log.dueAt": "Ngày đến hạn",
  "log.save": "Ghi nhận",
  "log.saving": "Đang ghi nhận…",

  "compose.reply": "Reply",
  "compose.relink": "Relink",
  "compose.draftWithAi": "Draft with AI",
  "compose.drafting": "Drafting…",
  "compose.discardDraft": "Discard draft",
  "compose.discardDraftHint":
    "Tells your Voice DNA this draft missed. The generated text is never kept.",
  "compose.aiDisclosureTitle": "AI-assisted draft",
  "compose.aiDisclosureFallback":
    "This draft was produced by AI. Read it and edit it before you send.",
  "compose.voiceVersion": "Built from your corpus · v{n}",
  "compose.provisional": "Provisional voice",
  "compose.provisionalHint":
    "Your Voice DNA is still being built. It already shapes this draft exactly as a finished one would — nothing is held back.",
  "compose.intent": 'Steer the draft (optional), e.g. "polite follow-up"',
  "compose.to": "To",
  "compose.cc": "Cc",
  "compose.subject": "Subject",
  "compose.body": "Body",
  "compose.purpose": "Consent purpose",
  "compose.purposeHint":
    "The send is allowed only if every recipient has granted consent for this purpose.",
  "compose.send": "Send",
  "compose.sendConfirmTitle": "Send this email?",
  "compose.sendBody":
    "You are sending this email now. This is an outbound, irreversible action.",
  "compose.sendMessageConfirmTitle": "Send this message?",
  "compose.sendMessageBody":
    "You are sending this message now. This is an outbound, irreversible action.",
  "compose.consentBlockedTitle": "Send blocked — no consent",
  "compose.consentBlocked":
    "A recipient has not granted consent for this purpose, so the send was suppressed (default-deny).",
  "compose.consentGoto": "Review consent",
  "compose.draftUnavailable":
    "AI drafting is unavailable (the model is not configured). You can still write the email yourself.",
  "compose.sendUnavailable":
    "Sending is unavailable (no mailer is configured).",
  "compose.mailboxNotSendCapable":
    "Your mailbox is connected for capture but was never granted permission to send. Reconnect it and approve sending — a mailbox connected before sending existed cannot be upgraded in place.",
  "compose.mailboxNotSendCapableGoto": "Reconnect your mailbox",
  "compose.sharedUnsubscribeToken":
    "A message carrying an unsubscribe link reaches one addressee at a time, because that link is the recipient's own consent record. Send it once per recipient, with no Cc.",
  "compose.multiRecipientWarning":
    "This purpose carries an unsubscribe link, so a send to more than one addressee will be refused. Send it once per recipient, with no Cc.",
  "compose.relinkTitle": "Relink this activity",
  "compose.relinkTarget": "Search a person, organization, deal, or lead",
  "compose.relinkReplace": "Move instead of also-link",
  "compose.relinkReplaceHint":
    "Replaces the existing link of the same type rather than adding another.",
  "compose.relinkConfirm": "Relink",
  "compose.emptyRecipients": "Add at least one recipient.",
  "compose.removeRecipient": "Remove {recipient}",
  "compose.actionFailed": "The request failed. Please try again.",

  "tasks.overdue": "Overdue",
  "tasks.today": "Today",
  "tasks.upcoming": "Upcoming",
  "tasks.undated": "No due date",
  "tasks.complete": "Done",
  "tasks.snooze": "Snooze 1d",
  "tasks.detail": "Task",
  "tasks.isDone": "Completed",
  "tasks.logged": "Logged",
  "tasks.new": "New task",
  "tasks.subject": "Subject",
  "tasks.dueDate": "Due date",
  "tasks.remindAt": "Remind me at",
  "tasks.remind": "Remind me",
  "tasks.reminder": "Reminder",
  "tasks.setReminder": "Set reminder",
  "tasks.clearReminder": "Clear reminder",

  "reports.sub": "deal theo giai đoạn — chưa trọng số cạnh có trọng số",
  "reports.count": "Deal",
  "reports.unweighted": "Chưa trọng số",
  "reports.weighted": "Có trọng số",
  "reports.planNote":
    "kế hoạch đã chạy và những dòng mà con số này đối chiếu về",
  "reports.reportDeals": "Deal theo giai đoạn",
  "reports.reportForecast": "Dự báo",
  "reports.reportOpenByCompany": "Deal đang mở theo công ty",
  "reports.forecastBanner":
    "Tổng theo nhóm là chưa trọng số; con số có trọng số trên bảng là Σ(giá trị × xác suất giai đoạn) — hai con số khác nhau, và đó là chủ ý.",
  "reports.company": "Công ty",
  "reports.openDeals": "Deal đang mở",
  "explain.sources": "Source rows",
  "explain.definition": "How this number is derived",

  "ai.sub": "bring your own agent — governed by the two-tier contract",
  "ai.fromPalette": "From the palette",
  "ai.tiers": "What an agent may do",
  "ai.tierAutoExecute": "Read & draft run instantly.",
  "ai.tierAutoExecuteDetail":
    "Lookups, summaries, drafts — visible, reversible, logged.",
  "ai.tierConfirmationRequired": "Write & send wait for you.",
  "ai.tierConfirmationRequiredDetail":
    "External sends and record changes stage into the inbox first.",
  "ai.connect": "Connect an agent",
  "ai.connectDetail":
    "Mint a passport in Settings and point any MCP-capable agent at your workspace. It reads only what you can see.",
  "ai.paletteHint": "Ask from anywhere with",

  "settings.identity": "Bạn",
  "role.admin": "Quản trị",
  "role.manager": "Quản lý",
  "role.rep": "Nhân viên kinh doanh",
  "role.readOnly": "Chỉ đọc",
  "role.ops": "Vận hành",
  "rbac.masked": "Giá trị đã che",
  "settings.saved": "Đã lưu.",
  "settings.passports": "Passport cho Agent",
  "settings.passportsSub":
    "Agent hành động với danh nghĩa của bạn, không bao giờ vượt quá bạn — mỗi lần gọi đều kiểm tra lại phân quyền của bạn",
  "passport.select": "Passport",
  "passport.noneOption": "Không dùng passport",
  "settings.passportsLendHint":
    "Đây là những passport của bạn để cho mượn. Kết nối một client MCP, nó sẽ hỏi bạn trao passport nào — kết nối đó sau đó mang đúng các phạm vi của passport ấy.",
  "settings.passportLabel": "Tên Agent",
  "settings.mint": "Tạo passport",
  "agents.connected": "Connected agents",
  "agents.connectedSub":
    "MCP clients holding their own credential, derived from a passport you lent",
  "agents.noneConnected": "No agent is connected yet.",
  "agents.connectedOn": "connected {date}",
  "agents.lentFrom": "lent from “{label}”",
  "agents.disconnect": "Disconnect",
  "agents.disconnectNamed": "Disconnect {client}",
  "agents.disconnected": "disconnected",
  "agents.lapsed": "credential expired",
  "agents.renewing": "renewing",
  "agents.renewsBy": "credential renews by {date}",
  "agents.expiredOn": "credential expired {date}",
  "agents.revokeGrant": "End connection",
  "agents.revokeGrantNamed": "End the connection to {client}",
  "agents.disconnectConfirm":
    "This ends the whole connection, not just one credential: the agent loses access on its next call and cannot renew. Reconnecting means lending a passport again.",
  "agents.connectHow": "Connect an agent",
  "agents.connectSteps":
    "Mint a passport above, then run one of these. The client registers itself and brings you back here to choose which passport to lend.",
  "agents.connectAntigravityPath":
    "Antigravity has no add command — put that block in ~/.gemini/config/mcp_config.json.",
  "agents.connectorOff": "The MCP connector is off for this installation.",
  "agents.connectorOffDetail":
    "No agent can connect until an operator enables it. Your passports still work as REST credentials.",
  "settings.tokenOnce": "Sao chép ngay — token này chỉ hiển thị một lần.",
  "settings.token": "token",
  "settings.autonomy": "Bậc tự chủ",
  "settings.autonomySub": "cái gì chạy ngay, cái gì chờ trong hộp phê duyệt",
  "settings.tierRead":
    "Đọc, tóm tắt, soạn nháp — chạy ngay, ghi nhật ký đầy đủ.",
  "settings.tierSend":
    "Gửi email, đặt lịch họp, thay đổi bản ghi — chờ bạn phê duyệt.",
  "settings.tierAdvance": "Chuyển giai đoạn của deal — luôn xác nhận trước.",
  "settings.locked": "đã khoá",
  "settings.purposes": "Mục đích chấp thuận",
  "settings.created": "tạo {date}",
  "settings.expires": "hết hạn {date}",
  "settings.revoked": "đã thu hồi",
  "settings.revoke": "Thu hồi",
  "settings.revokeConfirm":
    "Thông tin xác thực của passport này mất hiệu lực ngay — Agent sẽ mất quyền truy cập ở lần gọi kế tiếp.",
  "settings.automations": "Tự động hoá",
  "settings.automationsSub":
    "danh mục khởi đầu có giới hạn — bật, đặt tham số, tạm dừng",
  "settings.openAutomations": "Mở trình sửa tự động hoá",
  "settings.dangerZone": "Vùng nguy hiểm",
  "settings.dangerZoneSub":
    "chỉ dùng ngoài môi trường vận hành — không thể hoàn tác trên bản cài đặt này",
  "settings.resetDataDesc":
    "Đưa bản cài đặt này về trạng thái khởi động lần đầu. Dữ liệu nghiệp vụ và cấu hình workspace bị xoá sạch; tổ chức cùng người dùng của nó được giữ lại và vẫn đang đăng nhập.",
  "settings.resetDataButton": "Xoá sạch dữ liệu…",
  "settings.resetDataConfirmTitle": "Xoá sạch toàn bộ dữ liệu?",
  "settings.resetDataConfirmBody":
    "Nhập tên tổ chức của bạn để xác nhận. Không thể hoàn tác.",
  "settings.resetDataConfirmName": "Nhập đúng tên tổ chức này:",
  "settings.resetDataConfirmLabel": "Xác nhận tên tổ chức",
  "settings.audit": "Nhật ký kiểm toán",
  "audit.you": "Bạn",
  "audit.teammate": "Một đồng nghiệp",
  "audit.system": "Hệ thống",
  "audit.onBehalfOfYou": "thay mặt bạn",
  "audit.onBehalfOfTeammate": "thay mặt một đồng nghiệp",
  "settings.auditSub":
    "mọi hành động đều được quy trách — người, Agent hay connector",
  "settings.auditActor": "Tác nhân",
  "settings.auditEntity": "Loại thực thể",
  "settings.auditEntityId": "ID thực thể",
  "settings.auditAction": "Hành động",
  "settings.auditFrom": "Từ",
  "settings.auditTo": "Đến",
  "settings.auditExpand": "Xem chi tiết thay đổi",
  "settings.auditRule": "Quy tắc phân quyền",
  "settings.auditOnBehalf": "thay mặt",
  "settings.privacy": "Hộp yêu cầu quyền riêng tư",
  "settings.privacySub": "yêu cầu của chủ thể dữ liệu kèm thời hạn luật định",
  "settings.due": "hạn {date}",

  "privacy.addPurpose": "Thêm mục đích",
  "privacy.purposeKey": "Khoá",
  "privacy.purposeLabel": "Nhãn",
  "privacy.purposeDoi": "Cần xác nhận kép",
  "privacy.purposeCreate": "Tạo mục đích",
  "privacy.purposeAppendOnly":
    "Một mục đích đã tạo thì không đổi tên hay xoá được — danh mục chỉ thêm mới. Hãy chọn khoá thật cẩn thận.",
  "privacy.facetAll": "Tất cả",
  "privacy.overdue": "Quá hạn",
  "privacy.closed":
    "Đã đóng — một yêu cầu đã đóng không bao giờ mở lại. Mối lo mới là một yêu cầu mới.",
  "privacy.assignee": "Người xử lý",
  "privacy.assigneeUnassignable":
    "Đã đặt người xử lý thì không xoá được ở đây.",
  "privacy.resolution": "Kết luận",
  "privacy.resolutionRequired":
    "Đóng một yêu cầu thì phải có câu trả lời của nó.",
  "privacy.movedOn":
    "Yêu cầu này đã đi tiếp — người khác quyết định trước rồi. Hãy đọc lại bên dưới.",
  "privacy.inProgress": "Đang xử lý",
  "privacy.fulfil": "Thực hiện",
  "privacy.reject": "Từ chối",
  "privacy.newRequest": "Yêu cầu mới",
  "privacy.kind": "Loại",
  "privacy.person": "Người",
  "privacy.subjectRef": "Tham chiếu chủ thể",
  "privacy.dueAt": "Hạn",
  "privacy.openRequest": "Mở yêu cầu",
  "privacy.erasureNeedsPerson":
    "Một yêu cầu xoá dữ liệu phải nêu đích danh một người trong workspace này — thực hiện nó sẽ xoá bản ghi đó. Chủ thể dạng văn bản tự do thì không xoá được.",
  "privacy.accessManual":
    "Yêu cầu truy cập dữ liệu được thực hiện thủ công: hãy ghi lại những gì bạn đã gửi vào phần kết luận. Hệ thống này không tự tập hợp hay xuất dữ liệu thay bạn.",
  "privacy.fulfilErasureTitle": "Thực hiện yêu cầu xoá dữ liệu",
  "privacy.erasureIrreversible":
    "Thao tác này xoá vĩnh viễn người đó trên toàn hệ thống — bản ghi, hoạt động đã thu thập và các giá trị dẫn xuất. Không thể hoàn tác. Chính lần xoá này cũng được ghi vào nhật ký kiểm toán.",
  "privacy.typeErase": "Nhập ERASE để xác nhận",
  "privacy.erasureConfirm": "Xoá + chặn",
  "privacy.legalHold":
    "Bị chặn — lệnh lưu giữ pháp lý. Người này đang trong thời hạn lưu giữ theo luật, nên quyền xoá không thắng ở đây (Art. 17(3)(b)). Việc chặn áp dụng cho mọi vai trò, kể cả quản trị — không có ngoại lệ. Lần thử này đã được ghi vào nhật ký kiểm toán.",

  "settings.pipelines": "Pipeline",
  "settings.pipelinesSub": "Cấu hình pipeline và các giai đoạn của chúng.",
  "pipeline.new": "Pipeline mới",
  "pipeline.edit": "Sửa pipeline",
  "pipeline.name": "Tên",
  "pipeline.default": "Mặc định",
  "pipeline.notDefault": "Không mặc định",
  "pipeline.position": "Vị trí",
  "stage.new": "Giai đoạn mới",
  "stage.edit": "Sửa giai đoạn",
  "stage.name": "Tên",
  "stage.semantic": "Ngữ nghĩa",
  "stage.winProb": "Xác suất thắng",
  "stage.semOpen": "Đang mở",
  "stage.semWon": "Thắng",
  "stage.semLost": "Thua",

  "ob.read": "Read",
  "ob.confirm": "Confirm",
  "ob.url": "Website",
  "ob.urlScheme": "https://",
  "ob.back": "Back",
  "ob.finish": "Into the workspace",
  "ob.restoring": "Restoring your setup…",
  "ob.readKick": "Step 1 of 4 · company context",
  "ob.readTitle": "Your company",
  "ob.readSub": "Read it from your website, or type it in.",
  "ob.readChoice": "Choose how to describe your company",
  "ob.readWebsite": "Read my website",
  "ob.readWebsiteSub": "I'll research it; you approve every detail.",
  "ob.readManual": "Tell me yourself",
  "ob.readManualSub": "I'll ask one question at a time.",
  "ob.readTrustTitle": "I read public pages only. ",
  "ob.readTrustBody": "I save nothing until you confirm it.",
  "ob.coreIntroTitle": "First, I need to know your legal company.",
  "ob.coreIntroBody":
    "I need its legal identity, address and VAT or registration details. Then I'll learn what you sell, who you serve, and how you win business.",
  "ob.coreLegalKicker": "I start with legal identity",
  "ob.corePathLabel": "What I'll learn",
  "ob.corePathLegal": "Legal identity",
  "ob.corePathOffer": "Offer",
  "ob.corePathCustomer": "Customers",
  "ob.coreReadingPage": "I'm reading",
  "ob.coreWebsiteTitle": "Which website should I read?",
  "ob.coreWebsiteBody":
    "I'll find the legal notice first, then I’ll read your products, ideal customers, positioning and sales approach.",
  "ob.corePreparing": "I'm preparing to read {host}",
  "ob.coreLegalReading": "I'm reading the legal identity on {host}",
  "ob.coreLegalReadingBody":
    "I'm looking for the legal notice, registered organization, address and register or VAT/UID details. I leave anything unstated empty.",
  "ob.coreBusinessReading": "I'm learning how the business works",
  "ob.coreBusinessReadingBody":
    "I'm connecting products, customers and positioning to the exact public text that supports them.",
  "ob.coreReady": "I found {count} cited company details",
  "ob.corePartial": "I found {count} useful details, with some gaps",
  "ob.coreReadyBody":
    "I haven't saved anything yet. Please review the legal identity first, then the offer and ideal customer.",
  "ob.coreDeferredBody": "I'll resume this read automatically.",
  "ob.coreFailedBody":
    "I couldn't reliably access or ground this website, so I stopped instead of guessing. You can tell me the same information yourself.",
  "ob.coreFindingsTitle": "What I found and can support",
  "ob.coreFindingsBody":
    "I attach the public wording behind every value. If I couldn't verify a legal detail, I leave it empty.",
  "ob.ai.identity": "Hi, I'm Margince",
  "ob.ai.role": "Your company research AI",
  "ob.ai.speaker": "M",
  "ob.ai.speakerName": "Margince",
  "ob.ai.ready": "I'm ready to research",
  "ob.ai.configured": "Configured AI",
  "ob.ai.modelsUsed": "Models used in this task",
  "ob.ai.route": "Task · tier · provider",
  "ob.ai.calls": "AI calls",
  "ob.ai.tokens": "Tokens",
  "ob.ai.latency": "Model latency",
  "ob.ai.estimatedCost": "Estimated provider cost",
  "ob.ai.partialEstimate": "Partial · unpriced usage exists",
  "ob.ai.awaitingModel": "Shown after my first model call",
  "ob.ai.notAvailableYet": "Not available yet",
  "ob.ai.runtimeUnavailable": "Runtime details unavailable",
  // The runtime disclosure is a chip you can open rather than a permanent
  // band: cost is stated WHILE it is being spent, but a reader deciding
  // whether a legal entity is right should not have to read a billing table
  // to get to it.
  "ob.ai.runtimeChip": "What is answering, and what it costs",
  "ob.ai.answeringNow": "What is answering right now",
  "ob.ai.runScope": "This run only. The full log is in Settings → AI.",
  "ob.ai.tier.localSmall": "local, fast",
  "ob.ai.tier.cheapCloud": "cloud, efficient",
  "ob.ai.tier.premium": "premium reasoning",
  "ob.ai.tier.localLarge": "local, advanced",
  // The rail footer's plain-language line: the exact ids sit one click away
  // in the runtime chip's "Configured AI" row, so this says only what a
  // non-technical reader needs at a glance — how many models, and where.
  "ob.ai.summary.cloud.one": "1 model, running in the cloud",
  "ob.ai.summary.cloud.other": "{count} models, running in the cloud",
  "ob.ai.summary.local.one": "1 model, running locally",
  "ob.ai.summary.local.other": "{count} models, running locally",
  "ob.ai.summary.hybrid.one": "1 model, split between cloud and local",
  "ob.ai.summary.hybrid.other": "{count} models, split between cloud and local",
  "ob.ai.summary.development.one": "1 model, development mode",
  "ob.ai.summary.development.other": "{count} models, development mode",
  "ob.ai.summary.none": "No model configured yet",
  "ob.ai.summaryProviders.one": "1 provider configured",
  "ob.ai.summaryProviders.other": "{count} providers configured",
  "ob.ai.readFirst": "Start company setup before asking about it.",
  "ob.ai.liveArtifact": "Live, reviewable artifact",
  "ob.ai.companyKnowledge": "What I understand about your company",
  "ob.ai.companyKnowledgeBody":
    "Website evidence stays separate from our conversation. You decide what becomes company context.",
  "ob.ai.companyKnowledgeManualBody":
    "Your answers and my suggestions stay editable here. You decide what becomes company context.",
  "ob.ai.askPlaceholder":
    "Ask me about a finding, correct a detail, or tell me what I missed…",
  "ob.ai.send": "Send to Margince",
  "ob.ai.reviewBoundary":
    "I can suggest changes here. I only apply them to your draft when you approve.",
  "ob.ai.confirmBoundary":
    "Nothing becomes company context until you confirm this draft.",
  "ob.ai.confirmCompany": "Confirm and save company",
  "ob.ai.thinking": "I'm checking the dossier and preparing an answer…",
  "ob.ai.suggestedChanges": "Suggested changes to your draft",
  "ob.ai.applyChanges": "Apply to my draft",
  "ob.ai.applied": "Applied to draft",
  "ob.ai.finding": "cited finding",
  "ob.ai.findings": "cited findings",
  "ob.continueManual": "Tell me instead",
  "ob.reviewFindings": "Review what I found",
  "ob.live": "Live",
  "ob.readingHost": "I'm understanding {host}",
  "ob.readStatus.queued": "I'm getting ready",
  "ob.readStatus.deferred": "I'm waiting for AI budget",
  "ob.readStatus.reading": "I'm reading now",
  "ob.readStatus.ready": "I've finished reading",
  "ob.readStatus.partial": "I've finished with some gaps",
  "ob.readStatus.failed": "I need your help",
  "ob.readStatus.confirmed": "I've saved your choices",
  "ob.readStatus.abandoned": "I've stopped",
  "ob.phaseDiscover": "I'm discovering pages",
  "ob.phaseExtract": "I'm extracting cited facts",
  "ob.phaseReady": "I'm preparing your review",
  "ob.pagesRead": "pages I've read",
  "ob.legalEntitiesFound": "legal entities I've found",
  "ob.profileFindings": "profile details I've found",
  "ob.usefulFacts": "other facts I've found",
  "ob.coverageDetails": "What I covered and couldn't read",
  "ob.legalFoundTitle": "Legal entities I found",
  "ob.legalFoundBody":
    "I keep each legal block together: registered name, address and register or VAT/UID number. If the site names several entities, you'll choose yours in the review.",
  "ob.legalEntity": "Legal entity",
  "ob.confirmWebsite":
    "I grounded this in {count} public pages. Edit anything; untouched values keep their evidence.",
  "ob.confirmManual":
    "You told me this directly, so I'll store your answers as human assertions.",
  "ob.legalTitle": "Which legal entity should I use?",
  "ob.legalSub":
    "I found several entities in the legal notice. Choose yours and I'll fill in its details.",
  "ob.factsTitle": "Other facts I found",
  "ob.factsSelected": "{selected} of {total} selected",
  "ob.factsSub":
    "Untick anything that should not become company context — up to 100 facts can be selected.",
  "ob.nowUnderstands": "I now understand",
  "ob.contextReady":
    "I can now use this context for relevant drafts, search, agents and Voice DNA — with provenance attached.",

  "ob.s1.kick": "Step 2 of 5 · confirm",
  "ob.s1.title": "Review what I learned about your company",
  "ob.s1.sub":
    "I filled only what I could support from your website. Please correct anything that is wrong.",
  "ob.s1.urlPlaceholder": "yourcompany.com",
  "ob.s1.identityLabel": "Legal organization",
  "ob.s1.offerLabel": "Products and offer",
  "ob.s1.customerLabel": "Customer",
  "ob.s1.salesLabel": "Positioning and sales context",
  "ob.s1.fieldRequired": "Required.",
  "ob.s1.requiredMissing": "Fill these in before you continue: {fields}",
  "ob.s1.saving": "Saving…",
  "ob.s1.saveFailed": "Couldn't save your company",
  "ob.s1.savedNote":
    "Saved to your workspace. Change anything here and continue to save it again.",
  "ob.s1.omitLabel": "I didn't invent anything",
  "ob.s1.omitBody":
    "I filled only what I could quote from your site. You can add anything I couldn't verify.",
  "ob.readGo": "Read my website",
  "ob.trustPublic": "I only read your public website. No login required.",
  "ob.urlWillRead": "I'll read {host}",
  "ob.readFromSite": "read from site",
  "ob.failTitle": "I couldn't read enough from this website",
  "ob.tryAnother": "Try another URL",

  "ob.manualChapterLegal": "Your legal organization",
  "ob.manualChapterOffer": "Products and offer",
  "ob.manualChapterCustomer": "Ideal customer",
  "ob.manualChapterSales": "How you sell",
  "ob.manualNext": "Next question",
  "ob.manualLater": "Add later",
  "ob.manualReview": "Review my answers",
  "ob.manualRequired": "Required to create a usable company profile",
  "ob.manualOptional": "Optional — leave it empty to add later",
  "ob.manual.display_name": "What name do customers know your company by?",
  "ob.manual.display_nameHint":
    "Use the familiar company or trading name shown throughout Margince.",
  "ob.manual.legal_name": "What is the full registered legal name?",
  "ob.manual.legal_nameHint":
    "Include the legal form, such as GmbH, Ltd, Inc. or AG. Add it later if it does not apply.",
  "ob.manual.registered_address": "What is the registered address?",
  "ob.manual.registered_addressHint":
    "Use the official address from the commercial register or legal notice.",
  "ob.manual.register_vat": "What are the register and VAT/UID numbers?",
  "ob.manual.register_vatHint":
    "Enter the identifiers exactly as issued. Leave this empty when none applies.",
  "ob.manual.industry": "Which industry is the company in?",
  "ob.manual.industryHint":
    "Choose the description your customers would immediately recognize.",
  "ob.manual.history": "Is there useful company history Margince should know?",
  "ob.manual.historyHint":
    "For example founding year, origin or an important change in the business.",
  "ob.manual.offer_summary": "What products or services do you sell?",
  "ob.manual.offer_summaryHint":
    "One or two concrete sentences are enough. This is the commercial explanation Margince will use.",
  "ob.manual.value_proposition": "What outcome does the offer create?",
  "ob.manual.value_propositionHint":
    "Explain the value customers receive, not only the product features.",
  "ob.manual.usp": "What makes customers choose you?",
  "ob.manual.uspHint":
    "Name the strongest meaningful difference from the alternatives.",
  "ob.manual.icp": "Who is your ideal customer?",
  "ob.manual.icpHint":
    "Describe the companies or people that benefit most — size, industry, situation or geography.",
  "ob.manual.buying_center": "Who evaluates, buys or approves the purchase?",
  "ob.manual.buying_centerHint":
    "List the typical roles and who has the final say.",
  "ob.manual.customer_pains": "What problems bring those customers to you?",
  "ob.manual.customer_painsHint":
    "Use the problems customers would describe in their own words.",
  "ob.manual.desired_outcomes": "What are they trying to achieve?",
  "ob.manual.desired_outcomesHint":
    "Describe the practical or business outcomes they care about.",
  "ob.manual.buying_intents": "What usually signals buying interest?",
  "ob.manual.buying_intentsHint":
    "For example a new initiative, hiring pattern, deadline or operational problem.",
  "ob.manual.common_objections": "What objections do you hear most often?",
  "ob.manual.common_objectionsHint":
    "Include the concerns that regularly slow down or stop a purchase.",
  "ob.manual.sales_motion": "How does a typical sale happen?",
  "ob.manual.sales_motionHint":
    "Describe the path from first conversation to decision, including trials or procurement if relevant.",

  "ob.field.display_name": "Company name",
  "ob.field.offer_summary": "What do you sell?",
  "ob.field.icp": "Ideal customer",
  "ob.field.buying_center": "Who buys",
  "ob.field.value_proposition": "Value proposition",
  "ob.field.usp": "What sets you apart",
  "ob.field.customer_pains": "Customer pains",
  "ob.field.desired_outcomes": "Desired outcomes",
  "ob.field.buying_intents": "Buying intents",
  "ob.field.common_objections": "Common objections",
  "ob.field.sales_motion": "Sales motion",
  "ob.field.legal_name": "Registered legal name",
  "ob.field.registered_address": "Registered address",
  "ob.field.register_vat": "Register / VAT ID",
  "ob.field.industry": "Industry",
  "ob.field.history": "Company history",

  "ob.s3.kick": "Step 3 of 4",
  "ob.s3.title": "Look what you've built —",
  "ob.s3.titleEm": "with nothing connected.",
  "ob.s3.sub":
    "Your workspace already knows your business and your voice. Connect your inbox next and it fills itself with your real people, companies, and deals.",
  "ob.s3.subNoVoice":
    "Your workspace already knows your business. Connect your inbox next and it fills itself with your real people, companies, and deals.",
  "ob.s3.cardProfile": "Business profile",
  "ob.s3.cardProfileBody":
    "Confirmed and saved to your company page. Fields read from your site keep their source; the rest are in your own words.",
  "ob.s3.cardProfileSkippedBody":
    "Read back from your site but not saved yet — you skipped the confirm step. Go back and confirm to put it on your company page.",
  "ob.s3.cardVoice": "Your writing voice",
  "ob.s3.cardVoiceBody":
    "Built from the corpus you just gave us. Drafts will sound like you from day one.",
  "ob.s3.cardVoiceSkippedBody":
    "You skipped the voice step — drafts start in a neutral starter voice until you build yours. Two minutes, anytime, in Settings.",
  "ob.s3.cardPipeline": "Sales pipeline",
  "ob.s3.cardPipelineBody":
    "The standard 7-stage B2B template, pre-tuned to your industry. Empty until you connect — then deals fill in from your mail.",
  "ob.s3.cardDraft": "A sample draft, in your voice",
  "ob.s3.cardDraftExample": "A sample draft",
  "ob.s3.cardDraftBody": "See it below.",
  "ob.s3.exampleTag": "Illustrative example — not written from your data yet",
  "ob.s3.exampleProspect": "Nordwind Robotics",
  "ob.s3.draftSample":
    "Subject: Quick question on your assembly line\n\nHi {{name}} — saw {company} runs discrete assembly at scale. We help teams like yours get a working robotic cell running in 6 weeks without ripping out your existing PLCs. Worth a 15-minute look? Best, Lars",
  "ob.s3.originLabel": "Where this pipeline comes from",
  "ob.s3.originBody":
    "No magic — it's the standard B2B stage template, tuned to your industry from the Step-1 read. It's empty right now. When you connect your inbox, capture reads your sent mail and meetings, then proposes deals into these stages — each one evidenced and reversible. You approve what becomes a deal.",
  "ob.s3.stillNothing":
    "Still nothing connected. You're in control of when that changes.",

  "ob.s4.sub":
    "You've built the brain. Connect your inbox and your CRM fills itself — people, companies, and deals captured automatically, so you never type them.",
  "ob.s4.provGoogle": "Google",
  "ob.s4.provMicrosoft": "Microsoft",
  "ob.s4.provImap": "Any inbox (IMAP)",
  "ob.s4.microsoftBtn": "Allow access to my Microsoft",
  "ob.s4.microsoftHint":
    "Read-only mail access. You can disconnect any time from Settings.",
  "ob.s4.microsoftUnverified":
    'You may see an "unverified app" notice — that\'s this self-hosted install, not a third party.',
  "ob.s4.microsoftFailed": "The Microsoft connection didn't complete.",
  "ob.s4.connectOkTitle": "You're connected",
  "ob.s4.connectOkBody":
    "Your mailbox is linked. Capture begins on the next sync.",
  "ob.s4.connectVerifying": "Confirming the connection…",
  "ob.s4.connectLive": "Live and capturing",
  "ob.s4.connectConfirmFailed": "We couldn't confirm the connection.",
  "ob.s4.connectRetry":
    "Head to Settings → Integrations to try connecting again.",
  "ob.s4.connectDenied": "You declined access — nothing was connected.",
  "ob.s4.googleBtn": "Allow access to my Gmail",
  "ob.s4.soon": "Soon",
  "ob.s4.googleHint":
    "Read-only access. You'll approve it on Google's own consent screen, and you can disconnect it again any time.",
  "ob.s4.googleUnverified":
    "If Google shows an “unverified app” notice, choose Advanced → Continue. Margince only ever reads your mail — it never sends.",
  "ob.s4.googleOkTitle": "Gmail connected",
  "ob.s4.googleOkBody":
    "Capture is running in the background — new mail lands on your timeline within about a minute, and it keeps itself in sync from here on.",
  "ob.s4.googleLive": "Connection verified — background capture is on",
  "dedupe.title": "Possible duplicates",
  "dedupe.intro":
    "Pairs the capture pipeline flagged as likely the same person or company. Merging keeps both records' history; dismissing tells the system to never ask about this pair again.",
  "dedupe.loading": "Loading the review queue…",
  "dedupe.empty": "No duplicates waiting — the queue is clear.",
  "dedupe.confidence": "Match confidence:",
  "dedupe.field": "Field",
  "dedupe.left": "Keep left",
  "dedupe.right": "Keep right",
  "dedupe.kindPerson": "Person",
  "dedupe.kindOrganization": "Company",
  "dedupe.mergeCta": "Merge into selected",
  "dedupe.notDuplicateCta": "Not a duplicate",
  "dedupe.decided": "Decision saved.",
  "dedupe.undoCta": "Undo",
  "dedupe.undone": "Pair re-opened.",
  "dedupe.dismissNote": "Dismiss",
  "backfill.title": "Import your mail history",
  "backfill.intro":
    "Choose how far back to import. You'll see the scope and estimated cost before anything runs — and you can skip this entirely.",
  "backfill.windowLabel": "Import window",
  "backfill.window3m": "3 months",
  "backfill.window6m": "6 months",
  "backfill.window12m": "12 months",
  "backfill.previewCta": "Show me the scope first",
  "backfill.previewLoading": "Counting your mailbox…",
  "backfill.estimateMessages": "Messages in this window:",
  "backfill.estimateCost": "Estimated AI cost:",
  "backfill.estimateNote":
    "An estimate, not a bill — actual usage is metered and visible as it happens.",
  "backfill.startCta": "Start the import",
  "backfill.starting": "Starting…",
  "backfill.skip": "Skip the history import",
  "backfill.skippedNote":
    "No history imported. New mail is still captured from now on — you can start an import later from Settings.",
  "backfill.loading": "Checking import status…",
  "backfill.statusUnavailable":
    "The import status can't be read right now — capture itself keeps running.",
  "backfill.queuedTitle": "Import queued",
  "backfill.runningTitle": "Importing your mail history",
  "backfill.doneTitle": "History import complete",
  "backfill.errorTitle": "The import hit a problem",
  "backfill.cancelledTitle": "Import cancelled",
  "backfill.progressLabel": "Import progress",
  "backfill.countScanned": "Messages scanned",
  "backfill.countCaptured": "Captured",
  "backfill.statEmails": "Emails captured",
  "backfill.statPeople": "People",
  // The count is domains this run raised a company question for, not
  // companies created — a domain becomes one only if its site says so.
  "backfill.statCompanies": "Companies to check",
  "backfill.errorNote":
    "It will retry on its own; everything captured so far is kept.",
  "backfill.cancel": "Stop the import",
  "backfill.cancelledNote": "Stopped. Everything captured so far is kept.",
  "backfill.unsupportedNote":
    "This mailbox type can't be backfilled — only new mail is captured from now on.",
  "backfill.narrowingNote":
    "A wider window already ran for this mailbox; the import window can only be widened, not narrowed.",
  "backfill.staleUpdated": "Last updated {duration} ago — no recent progress.",

  // Connected inboxes (Settings → Integrations): the "manage in Settings"
  // surface the onboarding copy promises.
  "connectors.title": "Connected inboxes",
  "connectors.sub":
    "Mailboxes capturing into your CRM. Disconnect any one when you need to — already-captured records stay.",
  "connectors.loading": "Loading your connections…",
  "connectors.loadFailed": "Couldn't load your connections.",
  "connectors.empty": "No inbox is connected yet.",
  "connectors.connectCta": "Connect an inbox",
  "connectors.provGmail": "Gmail",
  "connectors.provGcal": "Google Calendar",
  "connectors.provGraph": "Microsoft",
  "connectors.provImap": "IMAP mailbox",
  "connectors.statusConnected": "Capturing",
  "connectors.statusPending": "Pending — not yet confirmed live",
  "connectors.statusReauth": "Needs reconnect",
  "connectors.statusError": "Sync error",
  "connectors.statusDisconnected": "Disconnected",
  "connectors.cannotSend": "Capturing only — cannot send",
  "connectors.reconnectToSend":
    "Reconnect this mailbox to send from it. A mailbox connected before sending existed cannot be upgraded in place — the provider only grants sending on a fresh connection.",
  "connectors.lastSynced": "Last synced {at}",
  "connectors.neverSynced": "Waiting for the first sync",
  "connectors.nextCheck": "Next check ~{at}",
  "connectors.polled": "Polled on a schedule (no push subscription)",
  "connectors.pushRenewal": "Push renewal by {at}",
  "connectors.notConfigured":
    "Mail capture isn't configured in this deployment.",
  "connectors.reconnect": "Reconnect",
  "connectors.disconnect": "Disconnect",
  "connectors.disconnectTitle": "Disconnect this inbox?",
  "connectors.disconnectBody":
    "This will delete the credential we stored for this mailbox. Capture stops immediately; everything already captured stays in your CRM, and reconnecting will ask for permission again.",
  "connectors.disconnectBodyGoogleNote":
    "Google may still list Margince under your account's third-party access — remove it there if you want to revoke it fully.",
  "connectors.disconnectBodyMicrosoftNote":
    "Microsoft may still list Margince among your account's connected apps — remove it there if you want to revoke it fully.",
  "connectors.errRateLimited":
    "The provider is throttling us. Capture is running slower than usual; nothing is lost.",
  "connectors.errUnreachable":
    "We couldn't reach the provider. We'll keep retrying.",
  "connectors.errAuth":
    "The provider rejected our credentials. Reconnect to resume.",
  "connectors.errHistoryGone":
    "The provider's change history expired. The next sync re-anchors from a fresh point.",
  "connectors.errInternal":
    "Something went wrong on our side. We stopped rather than capture partial data.",
  "connectors.errUnknown":
    "Capture hit a problem we can't classify yet. We'll keep retrying.",

  // The OAuth return outcome (Task 2): the callback lands back on
  // #/settings/integrations/{outcome} — a dismissible inline note driven by
  // that route segment, never a claim the server hasn't confirmed.
  "connectors.oauthOk": "Connected. Your mailbox is now capturing.",
  "connectors.oauthDenied": "You declined access — nothing was connected.",
  "connectors.oauthError":
    "The connection couldn't be completed — please try again.",
  // Two failures that "try again" would be wrong about: the provider refused
  // the grant (retrying the same way repeats it), and the provider's API isn't
  // enabled for this deployment (no user action can clear it).
  "connectors.oauthRejected":
    "The provider declined the connection. Make sure you accept every permission it asks for, then try connecting again.",
  "connectors.oauthMisconfigured":
    "This deployment can't complete that connection yet — the provider's API isn't enabled for it. An administrator needs to enable it; the server log names which API.",
  "connectors.dismissOutcome": "Dismiss",

  // The always-present "Add a connection" affordance (Task 1): the empty
  // state and the roster footer share the same not-yet-connected provider
  // buttons, so the copy that names them lives once here.
  "connectors.addConnection": "Add a connection",
  "connectors.googleSeparateNote":
    "Gmail and Google Calendar connect separately.",
  "connectors.providerNotConfigured":
    "{provider} isn't configured in this deployment.",

  // The inline IMAP connect form (Task 6): first-connect and reconnect for
  // the one credential provider, done in Settings instead of bouncing to
  // onboarding.
  "connectors.imapConnectCta": "Connect an IMAP mailbox",
  "connectors.imapModalTitle": "Connect an IMAP mailbox",
  "connectors.imapHost": "IMAP server",
  "connectors.imapPort": "Port",
  "connectors.imapUsername": "Email address",
  "connectors.imapSecret": "App password",
  "connectors.imapMailbox": "Mailbox",
  "connectors.imapMaxMessages": "Messages per sync",
  "connectors.imapSecretHint":
    "Use an app-specific password. We seal it in the credential vault and read your mail on a schedule until you disconnect — disconnecting deletes it.",
  "connectors.imapSubmitCta": "Connect",
  "connectors.imapLoginRejected":
    "The mailbox rejected these credentials. Check host, email and app password.",
  "connectors.imapUnreachable": "The mail server could not be reached.",

  // The Telegram connector panel (Task 17, design §9.1-§9.2): one bot
  // connects for the whole workspace — no OAuth handshake, a BotFather
  // token submitted through the same inline-form shape the IMAP connector
  // uses. Unlike the mail providers, the connection stays editable in
  // place: replacing the token goes through PATCH, never a disconnect.
  "connectors.provTelegram": "Telegram",
  "connectors.telegramTitle": "Telegram bot",
  "connectors.telegramSub":
    "One bot receives and sends messages for the whole workspace.",
  "connectors.telegramNotConfigured":
    "Messaging channels aren't configured in this deployment.",
  "connectors.telegramConnectCta": "Connect a Telegram bot",
  "connectors.telegramEditToken": "Replace token",
  "connectors.telegramDisconnectTitle": "Disconnect this bot?",
  "connectors.telegramDisconnectBody":
    "This deletes the stored token and stops checking the bot for new messages. Capture and sending stop immediately; everything already captured stays in your CRM.",
  "connectors.telegramModalTitle": "Connect a Telegram bot",
  "connectors.telegramEditTitle": "Replace the bot token",
  "connectors.telegramBotToken": "Bot token",
  "connectors.telegramBotTokenHint":
    "Paste the token BotFather gave you when you created the bot. We seal it in the credential vault and never show it again.",
  "connectors.telegramSubmitCta": "Connect",
  "connectors.telegramReplaceCta": "Replace token",
  "connectors.telegramConnectedAs": "Connected as @{username}.",

  // The workspace's own consumer-mail list (CAP-PARAM-5): what the shipped
  // baseline missed, and what it got wrong. Admin-curated and shared, because
  // whether a domain can name a company is a fact about the domain.
  "consumerMail.title": "Consumer mail domains",
  "consumerMail.sub":
    "Mail from a consumer mailbox still creates the person — it just never creates a company. Margince ships a list of these providers; add what it missed, or take back a domain it wrongly claimed.",
  "consumerMail.domainLabel": "Domain",
  "consumerMail.domainPlaceholder": "provider.example",
  "consumerMail.kindLabel": "What this domain is",
  "consumerMail.kind.extra": "Consumer mail — never a company",
  "consumerMail.kind.never": "A real company — ignore the shipped list",
  "consumerMail.add": "Add",
  "consumerMail.remove": "Remove",
  "consumerMail.none": "Nothing added. The shipped list decides every domain.",
  "consumerMail.adminOnly": "You do not have permission to change this list.",

  "ob.s4.googleVerifying": "Verifying the connection…",
  "ob.s4.googleDenied": "You declined the Google consent",
  "ob.s4.googleFailed": "The Google connection didn't complete",
  "ob.s4.googleRetry":
    "Nothing was stored. You can try again whenever you like — or connect over IMAP instead.",
  "ob.s4.imapHost": "IMAP host",
  "ob.s4.imapHostPlaceholder": "imap.gmail.com",
  "ob.s4.imapPort": "Port",
  "ob.s4.imapEmail": "Email",
  "ob.s4.imapPassword": "App password",
  "ob.s4.imapMailbox": "Mailbox",
  "ob.s4.imapMax": "How many recent emails",
  "ob.s4.imapHint":
    "Use an app-specific password (Gmail: Account → Security → App passwords). We seal it in the credential vault and keep reading new mail until you disconnect — disconnecting deletes it.",
  "ob.s4.imapConnect": "Test and connect",
  "ob.s4.connecting": "Connecting securely…",
  "ob.s4.scope1Lead": "We read — we don't clutter.",
  "ob.s4.scope1Rest":
    "Your mail becomes contacts, companies and activities, captured automatically.",
  "ob.s4.scope2Lead": "We never send anything without your approval.",
  "ob.s4.scope2Rest": "Drafts wait in your approval inbox.",
  "ob.s4.scope3Lead": "Your data stays in your workspace.",
  "ob.s4.scope3Rest": "Own-your-data — export or delete everything anytime.",
  "ob.s4.scope4Lead": "Disconnect in one click.",
  "ob.s4.scope4Rest": "The CRM keeps working; it just stops capturing.",
  "ob.s4.capturedTitle": "Mailbox connected",
  "ob.s4.capturedBody":
    "Sit back — your CRM is building itself. New mail keeps arriving here as the first sweep runs, usually within a couple of minutes.",
  "ob.s4.enterCrm": "Enter your CRM",
  "ob.s4.connectFailed": "Couldn't connect that mailbox",
  "ob.s4.notNow": "Not now",

  "ob.conv.threadLabel": "Onboarding conversation",
  "ob.conv.welcome":
    "Hi, I am Margince. I set up your CRM by reading what is already true about your business, and I show a source for everything I keep.",
  "ob.conv.welcomeMember":
    "Hi, I am Margince. Your team is already set up. Two short steps and you are in.",
  "ob.conv.read.started": "Reading {host} now. I will tell you what I find.",
  "ob.conv.read.pages": "Pages read so far: {pages}.",
  "ob.conv.read.learnedField": "Learned {field}: {value}",
  "ob.conv.read.extracting":
    "Done crawling. Now extracting what the site says about your business.",
  "ob.conv.read.warning": "Heads up: {warning}",
  "ob.conv.read.failed":
    "I could not read that site. Try another URL, or tell me directly.",
  "ob.conv.read.deferred":
    "The read is paused for now. I will pick it up again automatically.",
  "ob.conv.read.pollFailed":
    "I lost the connection while reading. What I already found is kept.",
  "ob.conv.clarify.intro":
    "One thing I need you to decide. The site is ambiguous here.",
  "ob.conv.clarify.entity":
    "The site names more than one legal entity. Which one is this installation for?",
  "ob.conv.review.ready":
    "I prepared the mapping. Review it and confirm what is right.",
  "ob.conv.company.confirmed":
    "Company profile confirmed. Everything I stored carries its source.",
  "ob.conv.manual.chosen": "I will type it in myself.",
  "ob.conv.voice.skipped": "Skip voice for now.",
  "ob.conv.voice.uploadAdded": "Added {name}.",
  "ob.conv.voice.speakerQuestion":
    "This transcript has several speakers. Which one is you? Only your own words count.",
  "ob.conv.voice.speakerOptionDetail": "words: {words} · turns: {turns}",
  "ob.conv.voice.guideSpeaker":
    "A speaker choice is waiting on the right — pick which one is you.",
  "ob.conv.voice.speakerFoot": "Your choice applies to this file only.",
  "ob.conv.voice.speakerContinue": "Use this speaker",
  "ob.conv.voice.continueSkippedStatus":
    "Skipped for now — add it later in Settings.",
  "ob.conv.voice.continueFailedStatus":
    "Your material is safe — retry now, or continue and pick this up later.",
  "ob.conv.voice.continueDeferredStatus":
    "No action needed here — continue, and it finishes on its own.",
  "ob.conv.voice.collectAsk":
    "Send me things you wrote. Call transcripts are best: .vtt, .srt, .json, or text with speaker labels. Plain documents work too.",
  "ob.conv.voice.composer": "Paste the text you wrote here",
  "ob.conv.voice.dropHint":
    "You can also drop files anywhere in this conversation.",
  "ob.conv.voice.fileSkipped":
    "I cannot read {name}. I take .txt, .md, .vtt, .srt, or .json.",
  "ob.conv.voice.fileEmpty":
    "There are no words in {name}, so nothing was counted.",
  "ob.conv.voice.reactionTranscript":
    "Words kept: {kept} of {total}. Only your turns count, and spoken register is exactly what sharpens your voice.",
  "ob.conv.voice.reactionDocument":
    "Words counted: {words}. Every word here is yours, so all of them count.",
  "ob.conv.voice.refusalUnattributed":
    "That looks like a conversation, but I cannot tell which words are yours. I counted nothing, because I only count words that are provably yours.",
  "ob.conv.voice.refusalSpeaker":
    "I could not find that speaker in the transcript. Nothing was counted.",
  "ob.conv.voice.refusalUnsupported":
    "I could not parse that file as text or a transcript. Nothing was counted.",
  "ob.conv.voice.ingestFailed": "I could not add that source: {detail}",
  "ob.conv.voice.ingestUnexpected":
    "I could not add that source. Try again in a moment.",
  "ob.conv.voice.pasteAdd": "Yes, add it to my corpus.",
  "ob.conv.voice.pasteDiscard": "No, discard it.",
  "ob.conv.voice.pasteSource": "Pasted text",
  "ob.conv.voice.buildFloor":
    "Own words so far: {words}. I need at least {min} before I can build.",
  "ob.conv.voice.buildNudge":
    "I have enough to build. More material still helps: 4,000 or more words make your voice noticeably sharper.",
  "ob.conv.voice.buildChip": "Build my voice profile",
  "ob.conv.voice.retryBuild": "Try the build again",
  "ob.conv.voice.buildPollFailed":
    "I lost the connection during the build. Your texts are kept; try the build again.",
  "ob.conv.voice.statusBuilding": "Building your voice profile",
  "ob.conv.voice.resultTitle": "Here is your voice, in your own words.",
  "ob.conv.voice.resultLoading": "Loading what the build learned.",
  "ob.conv.voice.resultEmpty":
    "The build finished, but there is nothing to show yet. You can review it in Settings.",
  "ob.conv.voice.candidateNote":
    "This version needs your review before it goes live. You can approve it in Settings.",
  "ob.conv.voice.artifactTitle": "Voice corpus",
  "ob.conv.voice.artifactBody":
    "Only your own words count here. Every number comes from the server after speaker filtering.",
  "ob.conv.voice.artifactEmpty":
    "Nothing collected yet. Attach a transcript or a text you wrote.",
  "ob.conv.voice.meterWords": "Own words: {words} of {target}",
  "ob.conv.voice.meterBand": "Quality: {band}",
  "ob.conv.voice.manifestKept": "Kept {kept} of {total} words",
  "ob.conv.voice.manifestWords": "{words} words",
  "ob.conv.voice.registerMix": "Registers: {mix}",
  "ob.conv.voice.stageTitle": "Build progress",
  "ob.conv.corpus.words": "Own words in your corpus now: {words}.",
  "ob.conv.corpus.band": "Corpus quality moved to {band}.",
  "ob.conv.build.snapshot": "Locking in your corpus.",
  "ob.conv.build.extract": "Finding your signature moves.",
  "ob.conv.build.evaluate": "Testing drafts against held-out samples.",
  "ob.conv.build.activate": "Activating your voice profile.",
  "ob.conv.build.succeeded": "Your voice profile is ready.",
  "ob.conv.build.deferred":
    "The build is queued behind budget. It will run automatically.",
  "ob.conv.build.failed":
    "The build did not finish. Your texts are kept and you can retry anytime.",
  "ob.conv.recap":
    "Here is what your CRM knows now, with a source for every item.",
  "ob.conv.consent":
    "Last step: what may I capture, and for which purpose? Nothing is on by default.",
  "ob.conv.done": "Setup complete. Your CRM is ready.",
  "ob.conv.composer": "Ask me, or paste your website",
  "ob.conv.clarify.question": "{question}",
  "ob.conv.clarify.optionDetail": "{detail}",
  "ob.conv.clarify.dismiss": "Skip this - I will set it myself",
  "ob.conv.clarify.keepMine": "Keep my value",
  "ob.conv.review.skipped": "You skipped: {fields}. Edit them any time.",
  "ob.conv.clarify.applyFailed":
    "I could not record that choice: {detail} Pick it again.",
  "ob.conv.clarify.applyMissing":
    "The server did not confirm that choice. Pick it again.",
  "ob.conv.loadFailed": "I could not check your setup. Please try again.",
  "ob.conv.retry": "Try again",
  "ob.conv.connect.persistFailed": "I could not record the finish. Try again.",
  "ob.conv.review.title": "Here is everything I found. Correct me.",
  "ob.conv.review.showMore": "Show full text",
  "ob.conv.review.showLess": "Show less",
  "ob.conv.review.continue": "Continue",
  "ob.conv.review.progressLabel": "Required fields completed",
  "ob.conv.review.requiredRemaining.one":
    "{count} field needed before you can continue",
  "ob.conv.review.requiredRemaining.other":
    "{count} fields needed before you can continue",
  "ob.conv.review.requiredDone": "Nothing more needed — you can continue.",
  "ob.conv.review.confirmQuestionOpen":
    "A decision is still open. Answer it to continue.",
  "ob.conv.triage.stateRequired": "required, still empty",
  "ob.conv.triage.stateEmpty": "empty",
  "ob.conv.triage.stateTyped": "typed by you",
  "ob.conv.triage.stateStored": "from your profile",
  "ob.conv.triage.emptyHint": "Not found on your site. Yours to add.",
  "ob.conv.triage.legalNotPublished":
    "Not stated on your legal or imprint page. Yours to add.",
  "ob.conv.triage.legalNotChecked":
    "I did not find a legal or imprint page on your site to check. Yours to add.",
  "ob.conv.triage.mapLabel": "Jump to a section",
  "ob.conv.triage.sectionBlocking": "{count} needed to continue",
  "ob.conv.triage.sectionAdvisory": "{count} worth a check",
  "ob.conv.triage.blockingHead": "Needed to continue",
  "ob.conv.triage.advisoryHead": "Worth a check",
  "ob.conv.triage.sectionSettled": "Nothing outstanding here",
  "ob.conv.triage.sectionMore": "+{count} more",
  "ob.conv.triage.restTitle": "Background, not work",
  "ob.conv.triage.looksSolid": "Looks solid · {count}",
  "ob.conv.triage.companyWebsite": "Website",
  "ob.conv.triage.sourceCount": "{count} src",
  "ob.conv.triage.peopleLabel": "People",
  "ob.conv.triage.peopleCount": "{count} found",
  "ob.conv.triage.peopleEmpty": "No people found on your site.",
  "ob.conv.triage.factsLabel": "Facts",
  "ob.conv.triage.factsCount": "{count} found",
  "ob.rail.spend": "Tokens this setup",
  "ob.rail.tokensUnit": "tok",
  "ob.conv.scene.step": "Step {n} of {m} · {label}",
  "ob.conv.scene.detour": "A quick detour",
  "ob.conv.scene.decisionSub":
    "Your site names more than one legal entity, and I will not guess which one signs your contracts: it decides what appears on every quote and invoice.",
  "ob.conv.scene.continue": "Continue",
  "ob.conv.scene.candidates": "{count} candidates",
  "ob.conv.connect.sceneTitle": "Connect your accounts.",
  "ob.conv.connect.sceneSub":
    "I build your contacts, companies and history out of what already exists in your inbox. No manual data entry, no CSV template.",
  "ob.conv.connect.mailboxTitle": "Your mailbox",
  "ob.conv.connect.mailboxHint":
    "Pick one. This is where your contacts, companies and history come from.",
  "ob.conv.connect.networkTitle": "Your network",
  "ob.conv.connect.networkHint":
    "Optional but worth it. Turns who you know into accounts and watches them for triggers.",
  "ob.conv.connect.required": "required",
  "ob.conv.connect.recommended": "recommended",
  "ob.conv.connect.gmailBrings": "Mail, contacts and calendar from Google",
  "ob.conv.connect.microsoftBrings":
    "Mail, contacts and calendar over the Graph API",
  "ob.conv.connect.imapBrings": "Any other mail host, with an app password",
  "ob.conv.connect.linkedinAuth": "Profile link, read only",
  "ob.conv.connect.scopeGoogle": "OAuth, read and send scopes",
  "ob.conv.connect.scopeMicrosoft": "OAuth, Graph API",
  "ob.conv.connect.scopeImap": "Any other provider, app password",
  "ob.conv.connect.connectCta": "connect →",
  "ob.conv.connect.connectedCta": "connected",
  "ob.conv.connect.blockedCard":
    "You already picked a mailbox. Disconnect it in Settings to switch.",
  "ob.conv.connect.guaranteesHeading": "What connecting actually does",
  "ob.conv.connect.railPromise":
    "We only read, and nothing sends without your approval.",
  "ob.conv.connect.dialogHeadlineAccess": "{name} access needed",
  "ob.conv.connect.dialogHeadlineImap": "Connect your mail host",
  "ob.conv.connect.dialogIntro":
    "{brings}. I read it once to build your contacts and history, then keep it in sync.",
  "ob.conv.connect.dialogClose": "Close",
  "ob.conv.connect.linkedinName": "LinkedIn",
  "ob.conv.connect.linkedinConnected": "Connected",
  "ob.conv.connect.linkedinSkippedNote": "Skipped: add it later in Settings",
  "ob.conv.connect.rosterFailedTitle": "Could not check your mailboxes",
  "ob.conv.connect.rosterFailedBody":
    "Something went wrong loading your connection status. Try again before picking a provider.",
  "ob.conv.voice.sceneTitle": "Teach me how you write.",
  "ob.conv.voice.sceneSub":
    "Every email, reply, and follow-up this CRM drafts goes out in your own words, not template English, and nothing sends until you approve it.",
  "ob.conv.voice.heroKicker": "Why this step matters",
  "ob.conv.voice.heroBody":
    "It learns your tone, rhythm, and phrasing from your own writing, and trains on yours alone — never anyone else's.",
  "ob.conv.voice.dropTitle": "Drop your writing here",
  "ob.conv.voice.dropSub":
    "Sent mail works best, because it shows how you write when you want something.",
  "ob.conv.voice.browse": "Browse files",
  "ob.conv.voice.pasteInstead": "Paste text instead",
  "ob.conv.voice.sourcesTitle": "Sources",
  "ob.conv.voice.meterLabel": "Progress toward the {min}-word minimum",
  "ob.conv.voice.meterProgress": "{words} of {min} words",
  "ob.conv.voice.meterReady":
    "{words} words — enough to build. More still sharpens it.",
  "ob.conv.voice.footReady":
    "Training takes about a minute. You will see a sample before anything is saved.",
  "ob.conv.voice.footFloor":
    "{min} words minimum. Below that the model just copies phrasing.",
  "ob.conv.voice.buildingTitle": "Learning your voice",
  "ob.conv.voice.buildingMeta": "{words} words, {sources} sources",
  "ob.conv.voice.resultSub":
    "Read the sample first. If it lands, confirm. If it is off, add more sources and I rebuild.",
  "ob.conv.voice.resultSubNoSample":
    "Your corpus is too small yet to hold out a sample draft. Here is what the build learned about how you write — add more sources for a sample.",
  "ob.conv.voice.resultContinue": "That is my voice",
  "ob.conv.voice.sampleEyebrow": "Sample, not sent",
  "ob.conv.voice.sampleAnother": "Another scenario",
  "ob.conv.voice.sampleSubjectLabel": "Subject",
  "ob.conv.voice.sampleWhyTag": "Why",
  "ob.conv.voice.dimensionsTitle": "Measured dimensions",
  "ob.conv.voice.dimensionsCount": "Measured: {count}",
  "ob.conv.voice.dimSentenceName": "Sentence length",
  "ob.conv.voice.dimSentencePoleLow": "Terse",
  "ob.conv.voice.dimSentencePoleHigh": "Elaborate",
  "ob.conv.voice.dimSentenceMeasured": "Measured",
  "ob.conv.voice.dimSentenceEvidence": "{count} words per sentence on average.",
  "ob.conv.scene.evidence": "evidence",
  "ob.conv.scene.hideEvidence": "hide evidence",
  "ob.conv.scene.whyThis": "What I read",
  "ob.conv.scene.foundOn": "Found on",
  "ob.conv.guide.decision":
    "I need one decision from you: {question} It is on the right, with the evidence for each option.",
  "ob.conv.guide.reviewBlocked.one":
    "Your review is ready on the right. {count} field blocks confirm.",
  "ob.conv.guide.reviewBlocked.other":
    "Your review is ready on the right. {count} fields block confirm.",
  "ob.conv.guide.reviewAdvisory.one":
    "Your review is ready on the right. Nothing blocks you; {count} thing is worth a look.",
  "ob.conv.guide.reviewAdvisory.other":
    "Your review is ready on the right. Nothing blocks you; {count} things are worth a look.",
  "ob.conv.guide.reviewClean":
    "Your review is ready on the right. It looks clean, check what you want and confirm when ready.",
  "ob.conv.guide.attentionHeading": "These need your input",
  "ob.conv.guide.attentionGroup.blocking": "Needed before you can continue",
  "ob.conv.guide.attentionGroup.decisions": "Needs a decision",
  "ob.conv.guide.attentionGroup.advisory": "Worth a look",
  "ob.conv.guide.attentionStatus.blocks": "needed to continue",
  "ob.conv.guide.attentionStatus.empty": "still empty",
  "ob.conv.guide.attentionStatus.decision": "needs a decision",
  "ob.conv.guide.attentionStatus.check": "worth a check",
  "ob.conv.activity.steps": "{count} steps",
  "ob.conv.showField": "Show me",
  "ob.conv.review.editDirectly": "Edit fields directly",
  "ob.conv.review.backToDossier": "Back to the dossier",
  "ob.conv.review.proposalFallback":
    "I could not load the prepared mapping. Review what I read directly; every field keeps its source.",
  "ob.conv.review.confirmFailed":
    "I could not save that yet: {detail} Fix it and accept again.",
  "ob.conv.review.confirmVersionSkew":
    "Your review just updated with newer information from the read. Have a look, then press Continue again.",
  "ob.conv.review.confirmVersionSkewStuck":
    "I checked again, but nothing has changed yet. Pressing Continue now would fail the same way, so have another look or check again in a moment.",
  "ob.conv.review.confirmNotReady":
    "This read is not ready to confirm yet. Wait for it to finish, or start a fresh one.",
  "ob.conv.artifact.empty":
    "Nothing read yet. Give me a website and this panel fills with sourced findings.",
  "ob.conv.results.continue": "Continue",
  "ob.conv.results.artifactTitle": "Setup recap",
  "ob.conv.results.artifactBody":
    "What your CRM starts with. Nothing here claims more than what actually happened.",
  "ob.conv.results.company":
    "Company profile confirmed for {name}. Everything stored carries its source.",
  "ob.conv.results.companyUnsaved":
    "Your company details are not saved yet. You can complete them later in Settings.",
  "ob.conv.results.voiceBuilt":
    "Your voice profile is built. Drafts will sound like you.",
  "ob.conv.results.voiceSkipped":
    "No voice profile yet. Drafts use a neutral starter voice, and you can build yours later in Settings.",
  "ob.conv.recap.back": "Welcome back. Here is where we stand.",
  "ob.conv.recap.company": "Your company profile for {name} is confirmed.",
  "ob.conv.recap.companyUnsaved":
    "Your company details are not saved yet. You can complete them in Settings.",
  "ob.conv.recap.voiceBuilt":
    "Your voice profile is built. Drafts can sound like you.",
  "ob.conv.recap.voiceSkipped":
    "You skipped the voice profile. Drafts use a neutral starter voice.",
  "ob.conv.recap.corpus":
    "Your corpus already holds {words} of your own words.",
  "ob.conv.recap.readTerminal":
    "Welcome back. I already finished reading {host}: {count} findings with sources. Your review is ready below.",
  "ob.conv.recap.readReading":
    "Welcome back. I am still reading {host}. Pages so far: {pages}.",
  "ob.conv.recap.readFailed":
    "Welcome back. My earlier read of {host} did not finish. Give me a website again, or tell me directly.",
  "ob.conv.recap.readDeferred":
    "Welcome back. My read of {host} is paused for now. Give me a website again, or tell me directly.",
  "ob.conv.connect.pick":
    "Pick a provider to see exactly what connecting does, or skip and connect later in Settings.",
  "ob.conv.connect.skip": "Skip connecting for now",
  "ob.conv.linkedin.cardBody":
    "Turns your network into accounts and contacts, and flags it when a connection changes jobs.",
  "ob.conv.linkedin.scope1Lead": "Your connection list \u2014",
  "ob.conv.linkedin.scope1Rest":
    "name, position, company and the date you connected.",
  "ob.conv.linkedin.scope2Lead": "Nothing else.",
  "ob.conv.linkedin.scope2Rest":
    "No messages, no posts, no who-viewed-you, no activity.",
  "ob.conv.linkedin.scope3Lead": "Your network stays yours.",
  "ob.conv.linkedin.scope3Rest":
    "It is attributed to you, never to the company, and disconnecting removes it.",
  "ob.conv.linkedin.scope4Lead": "Nobody is contacted.",
  "ob.conv.linkedin.scope4Rest":
    "Connecting sends no invitations and no messages, ever.",
  "ob.conv.linkedin.neverContacts":
    "Your connections do NOT become contacts — they exist only to answer one question: does anyone here already know someone at this company?",
  "ob.conv.linkedin.profileLabel": "Your LinkedIn profile URL",
  "ob.conv.linkedin.profilePlaceholder": "https://www.linkedin.com/in/…",
  "ob.conv.linkedin.profileWhy":
    "So the network is attributed to you by name \u2014 the CRM says \u201cAnna knows them\u201d, never \u201cthe company knows them\u201d.",
  "ob.conv.linkedin.authorize": "Authorize with LinkedIn",
  "ob.conv.linkedin.appPending":
    "Heads up: our LinkedIn app is still awaiting approval, so no connections sync yet — this only records your consent and profile. Upload Connections.csv under Settings, which works today.",
  "ob.conv.linkedin.skip": "Skip LinkedIn for now",
  "ob.conv.linkedin.connected":
    "LinkedIn authorized. Your connections will sync as soon as the app is approved.",
  "ob.conv.linkedin.skipped":
    "Skipped LinkedIn. You can connect it any time in Settings.",
  "ob.conv.connect.artifactTitle": "Inbox connection",
  "ob.conv.connect.artifactEmpty":
    "Pick a provider in the conversation and its connection panel opens here.",
  "ob.conv.next.decisionOne": "1 decision open",
  "ob.conv.next.build": "Ready to build your voice",

  // The setup rail: five stops, one word each. Long enough to name the step,
  // short enough that five of them fit a column at 10px.
  "ob.rail.read": "Read",
  "ob.rail.confirm": "Confirm",
  "ob.rail.voice": "Voice",
  "ob.rail.ready": "Ready",
  "ob.rail.connect": "Connect",

  // --- the gate: the first screen after sign-in -------------------------
  // One question and nothing else. Nobody should meet the whole tool on their
  // first screen, so the gate names what it will do, what it costs the reader
  // (two minutes), and who decides (they do) — then asks once.
  "ob.gate.title": "Hi {name}, I am the Margince AI.",
  "ob.gate.titleAnonymous": "I am the Margince AI.",
  "ob.gate.sub":
    "Give me your website and I will read it: what you sell, who buys from you, the people behind it. You review everything before it is saved, and nothing goes out without your yes. About two minutes.",
  "ob.gate.field": "Your website address",
  "ob.gate.placeholder": "yourcompany.com",
  "ob.gate.submit": "Read my site",
  "ob.gate.altPrompt": "No website handy?",
  "ob.gate.altAction": "Enter the details yourself",
  "ob.gate.invalidUrl":
    "That does not look like a web address. Try it as yourcompany.com.",
  // One string for two failures that look identical to the reader: the request
  // to start never landed, or the read started and did not finish. {detail} is
  // the server's own guidance and may be empty, so the sentence has to stand
  // without it.
  "ob.gate.startFailed":
    "I could not read that site. {detail} Try another address, or enter the details yourself.",
  // A deferred read is shelved, not broken: the server will come back to it. So
  // this says what is true and names both doors, without asking the reader to
  // fix anything.
  "ob.gate.readPaused":
    "That read is paused for now. {detail} It resumes on its own — or give me another address, or enter the details yourself.",

  // --- the read theatre -------------------------------------------------
  // Volume made visible. The wire gives no page-count denominator, so every
  // number here is an open count — never "14 of 18", never a bar with a known
  // end, because inventing the total would be inventing data.
  "ob.scan.title": "Reading {host}",
  "ob.scan.sub":
    "I am going through the site now. Every fact keeps the page it came from, so you can check anything I claim.",
  "ob.scan.doneTitle": "Read {host}",
  "ob.scan.doneSub":
    "{facts} facts and {fields} profile fields, each with the page it came from. Opening your review.",
  "ob.scan.phaseCrawling": "Fetching pages",
  "ob.scan.phaseExtracting": "Working out what you sell",
  "ob.scan.phaseQueued": "Queued, starting shortly",
  "ob.scan.phaseDeferred": "Paused for now",
  "ob.scan.pagesRead": "{pages} pages read",
  "ob.scan.pagesSkipped": "{count} skipped",
  "ob.scan.factsSoFar": "{count} facts so far",
  "ob.scan.stillReading": "still reading",
  "ob.scan.pageStripLabel": "Pages read so far",
  "ob.scan.logLabel": "The pages I am walking, newest first",
  "ob.scan.pageFetched": "{url} — read",
  "ob.scan.pageSkipped": "{url} — skipped: {reason}",
  "ob.scan.pageFailed": "{url} — could not be read: {reason}",
  "ob.scan.pageNoReason": "no reason recorded",
  "ob.scan.pageStatusFetched": "read",
  "ob.scan.pageStatusSkipped": "skipped: {reason}",
  "ob.scan.pageStatusFailed": "could not be read: {reason}",
  "ob.scan.transparency": "Transparency",
  "ob.scan.costLine": "{calls} calls · {tokens} tokens · {cost}",
  "ob.scan.costPending": "no model calls billed yet",
  "ob.scan.costUnpriced": " · unpriced usage exists",

  // --- the live panel: the sourced record building itself ----------------
  "ob.live.headReading": "Reading {host}",
  "ob.live.headDone": "Read {host}",
  "ob.live.nothingSaved":
    "Nothing is saved yet. I will show you everything when I am done.",
  "ob.live.summaryHeading": "Here is what I understood",
  "ob.live.summaryYouAre": "You are",
  "ob.live.summaryYouSell": "You sell",
  "ob.live.summaryYouSellTo": "You sell to",
  "ob.live.summaryVolume":
    "{facts} facts from {pages} pages, already filled in. Open any section to check it.",
  "ob.live.stepWebsite": "From reading your website",
  "ob.live.stepVoice": "Your writing voice",
  "ob.live.stepConnect": "Inbox and calendar",
  "ob.live.stateDone": "done",
  "ob.live.stateNow": "in progress",
  "ob.live.stateWaiting": "waiting",
  "ob.live.review": "Review",
  "ob.live.hide": "Hide",
  "ob.live.countFields": "{count} fields",
  "ob.live.countFacts": "{count} facts",
  "ob.live.countPeople": "{count} lead proposals",
  "ob.live.countPages": "{read} read · {skipped} skipped",
  "ob.live.cardIdentity": "Company identity",
  "ob.live.cardPositioning": "Positioning and sales angle",
  "ob.live.cardPeople": "People found",
  "ob.live.cardCoverage": "What I read, and what I skipped",
  "ob.live.cardVoice": "Voice profile",
  "ob.live.cardConnect": "Connected",
  "ob.live.voiceNotBuilt": "not built yet",
  "ob.live.connectNone": "nothing connected",
  "ob.live.noValue": "—",
  "ob.live.peopleEmpty":
    "Nobody yet. I only propose a person when the page gives a name and a role.",
  "ob.live.coverageWarning": "Warning",
  "ob.live.coverageSkipped": "Skipped",
  "ob.live.coverageFailed": "Could not read",
  "ob.live.coverageClean":
    "Every page I tried came back. Nothing was skipped and nothing failed.",

  // --- facts: preview card and the full table ----------------------------
  "ob.facts.title": "Facts",
  "ob.facts.catCompany": "Company",
  "ob.facts.catOffering": "Offering",
  "ob.facts.catMarket": "Market",
  "ob.facts.catSignal": "Signal",
  "ob.facts.catAll": "All",
  "ob.facts.mixLabel": "Facts by category",
  "ob.facts.selected": "{selected} of {total} will be saved",
  "ob.facts.selectAll": "Select all",
  "ob.facts.clearAll": "Clear all",
  "ob.facts.previewNote": "Showing the {count} highest-confidence facts.",
  "ob.facts.openTable": "Open the full table",
  "ob.facts.tableTitle": "All facts I read",
  "ob.facts.search": "Search facts",
  "ob.facts.hits": "{hits} of {total}",
  "ob.facts.colSave": "Save",
  "ob.facts.colCategory": "Category",
  "ob.facts.colFact": "Fact",
  "ob.facts.colSource": "Source",
  "ob.facts.colConfidence": "Confidence",
  "ob.facts.rowSave": "Save the fact: {fact}",
  "ob.facts.noMatch": "Nothing matches that search.",
  // The card only mounts once the read has settled, so its empty state reports a
  // finished search that found nothing — not one still in progress.
  "ob.facts.empty":
    "I read the site but pulled no separate facts out of it. What I did learn is in the sections above, each with its source.",
  "ob.facts.close": "Done",
  "ob.facts.closeTable": "Close the table",
  "ob.facts.capReached":
    "You can save up to {max} facts. Clear one to make room for another.",

  // --- the payoff: what two minutes actually bought ----------------------
  // Counts, not congratulation. Every cell is a real number off the wire, and
  // a cell with no number says so rather than showing a zero that looks earned.
  // Two leads for one moment. The first is only true when the install really was
  // empty minutes ago; a setup picked up across days is the supported path, and
  // the payoff above all else may not overstate.
  "ob.payoff.lead": "Minutes ago this was an empty install.",
  "ob.payoff.leadResumed": "This started as an empty install.",
  "ob.payoff.factsRead": "facts read",
  "ob.payoff.factsConfirmed": "facts you confirmed",
  "ob.payoff.peopleFound": "people found",
  "ob.payoff.profileFields": "profile fields",
  "ob.payoff.voiceWords": "words in your voice",
  "ob.payoff.pagesRead": "pages read",
  "ob.payoff.voiceNotTrained": "voice not trained yet",
  "ob.payoff.body":
    "Everything in there is yours to correct, and every value still points at the page it came from.",
  "ob.payoff.defaults":
    "Two defaults, both changeable in Settings → Autonomy: I prepare and wait for your confirmation, and I never overwrite a field you typed yourself.",
  "ob.payoff.seats":
    "The one thing left is your colleagues. Seats are billed, so you create them in Settings → People.",
  "ob.payoff.understood": "Understood",

  // --- the handoff into the app -----------------------------------------
  "ob.enter.cta": "Enter Margince",
  "ob.enter.assembling": "Assembling your workspace",

  // --- the mailbox backread ---------------------------------------------
  // A separate operation from connecting, and the copy has to keep them
  // separate: connecting grants access, the backread spends budget reading
  // history. Read-only, and it writes nothing until the reader approves.
  "ob.backread.heading": "How far back should I read?",
  "ob.backread.window3m": "3 months — recent context",
  "ob.backread.window6m": "6 months — recommended",
  "ob.backread.window12m": "12 months — full sales cycle",
  "ob.backread.estimate": "About {messages} messages in that window.",
  "ob.backread.estimateHeuristic":
    "Estimated from the mailbox, not counted yet.",
  "ob.backread.estimateCost": "Roughly {cost} in model calls.",
  "ob.backread.estimateFailed":
    "I could not estimate that window: {detail} You can still start, or pick another.",
  "ob.backread.note":
    "The backread is read-only. I import people, companies and activity, and I show you what I found before anything is written.",
  "ob.backread.start": "Connect and read",
  "ob.backread.startFailed":
    "I could not start the backread: {detail} Try again, or continue and start it later in Settings.",
  "ob.backread.running": "Reading your mailbox",
  "ob.backread.runningNote":
    "You can leave this running and keep working. I pick it up where I left off.",
  "ob.backread.queued": "Queued. It starts in a moment.",
  "ob.backread.progress": "{scanned} of about {total} messages",
  "ob.backread.progressNoTotal": "{scanned} messages so far",
  "ob.backread.tallyMessages": "messages read",
  "ob.backread.tallyCaptured": "kept",
  "ob.backread.tallySkipped": "ignored",
  "ob.backread.tallyPeople": "people found",
  "ob.backread.tallyCompanies": "companies found",
  "ob.backread.doneHeading": "Here is what is in there.",
  "ob.backread.doneNote":
    "Nothing is written yet. Everything I found waits for your review in the inbox.",
  "ob.backread.failed":
    "The backread stopped: {detail} Your connection is fine — you can start it again in Settings.",
  "ob.backread.cancelled": "I stopped reading. Nothing was written.",
  "ob.backread.cancelledPartial":
    "I stopped reading. What was already captured stays — it is waiting for you in the inbox.",
  "ob.backread.cancelFailed":
    "I could not stop the read: {detail} Try again — it keeps running meanwhile.",
  "ob.backread.detailUnavailable": "Something unexpected went wrong.",
  "ob.backread.cancel": "Stop reading",
  "ob.backread.explore": "Explore Margince meanwhile",
  "ob.backread.skip": "Do not read history now",

  "auth.title": "Margince",
  "auth.checking": "Đang kiểm tra phiên của bạn…",
  "auth.pageTitle": "Đăng nhập · Margince",
  "auth.loginTitle": "Đăng nhập Margince",
  // Short declaratives rather than one comma-joined sentence (VOICE-RULE-1):
  // each clause is a separate fact about the installation, and a reader who
  // stops after the first has still read a true sentence. Two of them, not
  // three — "Margince runs on your own server" is a claim about the product,
  // and someone at a login screen is here to get in. What is left is the
  // provisioning fact (A97, invite-only): what to do when there is no sign-up
  // link.
  "auth.loginSub":
    "Tài khoản do quản trị viên của bạn cấp. Không có đăng ký tự do.",
  "auth.coreDisclosure": "Margince · hệ thống AI",
  "auth.coreBoundary":
    "Tôi chỉ dùng được ngữ cảnh của bạn sau khi Margince xác minh đúng là bạn.",
  // The scope of the context the statement above is about. Bounded on purpose:
  // "nothing else" is what keeps it a limit rather than a list of capabilities,
  // which is what the artifact's version of this line was.
  "auth.coreScope":
    "Ngữ cảnh đó là thư của bạn, lịch của bạn, và những gì tôi đọc được trên web mở. Không gì khác, và không gì khi chưa được bạn cho phép.",
  "auth.corePermission": "Tôi dùng đúng quyền của bạn.",
  "auth.coreCites": "Tôi dẫn nguồn những gì tìm được.",
  "auth.coreWaits": "Tôi chờ trước khi thực hiện hành động ra bên ngoài.",
  // The fourth limit. The mockup's five became four, and one that did not
  // travel says why: "enriches records from sources it names" is a capability
  // claim, and ADR-0076 Decision 2 admits only limits. This one is a limit.
  "auth.coreMarks": "Tôi đánh dấu mọi giá trị do tôi ghi.",
  "auth.coreConfigured": "Đã cấu hình",
  "auth.coreUnconfigured": "AI chưa được cấu hình",
  "auth.coreStillWorks": "CRM vẫn hoạt động.",
  "auth.coreDevelopment": "AI chế độ phát triển",
  "auth.coreModeCloud": "định tuyến qua cloud",
  "auth.coreModeLocal": "định tuyến cục bộ",
  "auth.coreModeHybrid": "định tuyến kết hợp",
  "auth.coreModeNone": "không định tuyến mô hình",
  "auth.coreModeDevelopment": "đường phát triển ngoại tuyến",
  "auth.coreProviderAnthropic": "Anthropic",
  "auth.coreProviderGemini": "Gemini",
  "auth.coreProviderOllama": "Ollama",
  "auth.coreProviderOpenAI": "OpenAI",
  "auth.coreProviderCompatible": "nhà cung cấp tương thích",
  "auth.coreProviderVllm": "vLLM",
  // The shortest label that still names the field (VOICE-RULE-1), pinned by the
  // login spec §7.1/§7.2 (Amendment 4) and reconciling
  // single-organization-auth-concept.md §12, which already drew "Email".
  "auth.email": "Email",
  // A placeholder is an EXAMPLE, never an instruction and never the label
  // again. "Enter your email" in a placeholder is a label that disappears.
  // The address is the login spec §7.2's, and the reserved example domain
  // rather than a plausible one: `company.com` belongs to somebody.
  "auth.emailPlaceholder": "name@example.com",
  "auth.password": "Mật khẩu",
  "auth.passwordPlaceholder": "Mật khẩu",
  "auth.passwordHint": "ít nhất 12 ký tự",
  "auth.showPassword": "Hiện mật khẩu",
  "auth.hidePassword": "Ẩn mật khẩu",
  "auth.capsLock": "Caps Lock đang bật",
  // NOT the label of a served provider button. A real installation's button text
  // is `oidc_providers[].label` off the wire, server-owned, and the client never
  // composes it. This is what the ui-preview fixture uses to stand in for that
  // server in the reader's own language — see app/ui-preview.ts.
  "auth.continueWith": "Tiếp tục với {brand}",
  // Labels the password path, not the provider buttons above it: where the
  // installation runs SSO, the form beneath this divider is the fallback door.
  "auth.orWithEmail": "hoặc bằng email",
  // §7.1 verbatim. The noun is "organization", not "workspace": ADR-0061/A107
  // keeps `workspace` internal and §7.3 removed it from authentication. And the
  // line states that ACCESS is restricted, never that data is safe, encrypted or
  // compliant — VOICE-RULE-7 rules those out here, because they are outcome
  // claims the installation's own configuration can contradict, on the screen a
  // CISO reads on the way in.
  "auth.legalProtected": "Quyền truy cập tổ chức này bị giới hạn.",
  "auth.legalTerms": "Điều khoản",
  "auth.legalPrivacy": "Quyền riêng tư",
  "auth.signIn": "Đăng nhập",
  "auth.signingIn": "Đang đăng nhập…",
  "auth.failed": "Không thành công",
  "auth.errCredentials":
    "Không thể đăng nhập. Hãy kiểm tra email và mật khẩu rồi thử lại.",
  "auth.errRateLimited":
    "Quá nhiều lần thử đăng nhập. Hãy đợi một lát rồi thử lại.",
  "auth.errUnreachable":
    "Không kết nối được tới Margince. Hãy kiểm tra kết nối rồi thử lại.",
  "auth.retry": "Thử lại",
  "auth.noticeSignedOut": "Bạn đã đăng xuất.",
  "auth.noticeSessionExpired":
    "Phiên của bạn đã hết hạn. Hãy đăng nhập lại để tiếp tục.",
  "auth.connectionTitle": "Không kết nối được tới Margince",
  "auth.connectionBody":
    "Hãy kiểm tra kết nối rồi thử lại. Nếu vẫn vậy, có thể máy chủ đang khởi động lại.",
  "auth.unavailableTitle": "Bản cài đặt chưa sẵn sàng",
  "auth.unavailableBody":
    "Bản cài đặt Margince này chưa sẵn sàng để bạn đăng nhập. Cần người vận hành hoàn tất hoặc sửa lại phần thiết lập.",
  "auth.forgotLink": "Quên mật khẩu?",
  "auth.forgotTitle": "Đặt lại mật khẩu",
  // Two sentences, sentence-cased, with no dash. VOICE-RULE-5 forbids an em or
  // en dash anywhere in user-facing copy, and a lowercase opening mid-surface
  // reads as a fragment rather than as a sentence. Same for auth.resetSub.
  "auth.forgotSub":
    "Nhập email của bạn. Nếu địa chỉ đó có tài khoản, liên kết đặt lại đang trên đường tới.",
  "auth.sendResetLink": "Gửi liên kết đặt lại",
  "auth.forgotSentTitle": "Hãy kiểm tra hộp thư",
  "auth.forgotSentBody":
    "Nếu địa chỉ đó có tài khoản, liên kết đặt lại đang trên đường tới. Liên kết hết hạn sau một giờ.",
  "auth.resetTitle": "Chọn mật khẩu mới",
  "auth.resetSub": "Liên kết của bạn còn hiệu lực. Hãy chọn mật khẩu mới.",
  "auth.newPassword": "Mật khẩu mới",
  "auth.setNewPassword": "Đặt mật khẩu mới",
  "auth.resetFailed":
    "Liên kết đặt lại đó không hợp lệ, đã dùng, hoặc đã hết hạn.",
  // The password was refused, not the link — so the link is still good and the
  // user must not be sent to replace it.
  "auth.resetRejectedPassword":
    "Mật khẩu đó bị từ chối. Hãy chọn mật khẩu khác rồi thử lại.",
  // Neither the link's fault nor the user's: the token is untouched, so retrying
  // the same one is the right advice. Two sentences, no dash (VOICE-RULE-5).
  "auth.resetServerFailed":
    "Hiện chưa đặt được mật khẩu cho bạn. Liên kết vẫn còn hiệu lực, nên hãy thử lại sau giây lát.",
  // Its own key rather than auth.errRateLimited, which says "sign-in attempts":
  // this user is setting a password, not signing in, and copy that names the
  // wrong action reads as the wrong error.
  "auth.resetRateLimited":
    "Quá nhiều lần thử. Hãy đợi một lát rồi đặt lại mật khẩu.",
  "auth.requestNewLink": "Yêu cầu liên kết mới",
  "auth.askAdminForNewLink":
    "Hãy hỏi quản trị viên của bạn để lấy liên kết đặt mật khẩu mới.",
  "auth.resetDoneTitle": "Đã cập nhật mật khẩu",
  "auth.resetDoneBody":
    "Mật khẩu của bạn đã đổi và mọi phiên khác đều đã đăng xuất. Hãy đăng nhập bằng mật khẩu mới.",
  "auth.backToLogin": "Quay lại đăng nhập",
  "auth.signOut": "Đăng xuất",

  "client.back": "Quay lại Margince",
  "client.title": "Margince ngay cạnh hộp thư của bạn",
  "client.sub":
    "bề mặt tiện ích mở rộng — không khung ứng dụng, biết bản ghi đang xem",
  "client.sender": "Người gửi",
  "client.lookup": "Tra cứu",
  "client.open360": "Mở màn hình 360",
  "client.unknown": "Chưa có trong workspace của bạn.",
  "client.unknownDetail":
    "Người gửi này không khớp contact nào bạn xem được. Không có gì được lấy từ nơi khác.",
  "client.createLead": "Ghi nhận thành lead",
  "client.isolation": "chỉ nói chuyện với workspace CỦA BẠN",
  "client.attribution":
    "Mọi lượt ghi nhận đều được quy trách và kiểm toán được.",

  "book.title": "Book a meeting",
  "book.sub": "live availability from the connected calendar",
  "book.min15": "15 min",
  "book.min30": "30 min",
  "book.min60": "60 min",
  "book.attendee": "Attendee email",
  "book.welcomeBack": "Recognized: {name}",
  "book.subject": "Meeting via Margince",
  "book.confirmed": "Booked. The invite is on its way.",
  "book.failed": "Booking didn't go through — nothing was scheduled.",
  "book.publicSub": "pick a slot — no account needed",
  "book.name": "Your name",
  "book.email": "Your email",
  "book.consentWording":
    "I agree that my name and email are stored to arrange and follow up on this meeting.",

  "prefs.title": "Chọn những gì bạn nhận từ chúng tôi",
  "prefs.sub":
    "Mỗi mục đích là riêng biệt — không phải chuyện được tất cả hoặc không gì cả. Thư giao dịch không tắt được ở đây, vì bạn cần chúng; mọi thứ còn lại là quyền của bạn.",
  "prefs.invalidLink":
    "Liên kết này không còn hiệu lực. Liên kết tuỳ chọn sẽ hết hạn và có thể bị thu hồi — hãy lấy một liên kết mới từ bất kỳ email gần đây nào.",
  "prefs.rateLimited":
    "Vừa có quá nhiều lần thử từ đây. Hãy đợi một phút rồi tải lại.",
  "prefs.subscribed": "Đã đăng ký",
  "prefs.notSubscribed": "Chưa đăng ký — bạn không nhận gì cho mục đích này",
  "prefs.alwaysOn": "luôn bật",
  "prefs.lockedWhy": "Thư giao dịch — không thuộc diện từ chối nhận.",
  "prefs.notSaved": "Chưa lưu.",
  "prefs.savePending": "Đang chờ: {changes}.",
  "prefs.saveProof":
    "Chúng tôi ghi lại đúng nội dung bạn đã đọc kèm dấu thời gian làm bằng chứng — rồi nó áp dụng cho mọi lần gửi về sau.",
  "prefs.save": "Lưu tuỳ chọn",
  "prefs.discard": "Bỏ thay đổi",
  "prefs.partialSave":
    "Có gì đó hỏng giữa chừng. Một phần lựa chọn của bạn có thể đã được lưu — chúng tôi đã tải lại thiết lập hiện tại để bạn thấy đúng mình đang ở đâu.",
  "prefs.wordingGeneric": '"Hãy gửi cho tôi {label}."',
  "prefs.wording.marketing_email":
    '"Hãy gửi cho tôi tin cập nhật sản phẩm & email tiếp thị thỉnh thoảng."',
  "prefs.wording.events": '"Hãy gửi cho tôi lời mời sự kiện & webinar."',
  "prefs.unsubscribeAll": "Huỷ đăng ký mọi thư tiếp thị",
  "prefs.unsubscribeAllHint":
    "Muốn dừng toàn bộ thư không thiết yếu cùng lúc? Bạn vẫn nhận được thư giao dịch.",
  "prefs.oneClickDone":
    "Xong — bạn đã ra khỏi danh sách email tiếp thị. Việc này có hiệu lực ngay trên mọi chiến dịch.",
  "prefs.oneClickAlreadyOff": "Không cần làm gì — những mục này vốn đã tắt.",
  "prefs.undo": "Hoàn tác — vẫn nhận thư tiếp thị",
  "prefs.undoExplicit":
    "Đăng ký lại là một sự đồng ý rõ ràng — chúng tôi không âm thầm bật lại. Hãy lưu bên dưới để ghi nhận sự chấp thuận của bạn, hoặc bỏ thay đổi.",

  "auto.sub": "a closed catalog — pick a type, set its parameters, enable it",
  "auto.readOnly":
    "Read-only view — you do not have permission to change automations.",
  "auto.catalog": "Starter library",
  "auto.catalogSub": "the closed set of automation types",
  "auto.instances": "Configured automations",
  "auto.use": "Use template",
  "auto.name": "Name",
  "auto.create": "Create",
  "auto.createdPaused": "Created paused — nothing runs until you enable it.",
  "auto.enable": "Enable",
  "auto.pause": "Pause",
  "auto.delete": "Delete",
  "auto.statusEnabled": "enabled",
  "auto.statusPaused": "paused",

  "auto.runs.open": "Runs",
  "auto.runs.title": "Run history",
  "auto.runs.filterAll": "All",
  "auto.runs.filterFired": "Fired",
  "auto.runs.filterFailed": "Failed",
  "auto.runs.filterBlocked": "Blocked",
  "auto.runs.filterSkipped": "Skipped",
  "auto.runs.filterQueued": "Queued for approval",
  "auto.runs.empty": "This automation hasn't fired yet.",
  "auto.runs.emptyFiltered": "No runs with this outcome.",
  "auto.runs.needsApproval": "needs approval",
  "auto.runs.why": "Why",
  "auto.runs.target": "Target",
  "auto.runs.result": "Result",
  "auto.runs.reason": "Reason",
  "auto.runs.outcomeFired": "fired",
  "auto.runs.outcomeFailed": "failed",
  "auto.runs.outcomeBlocked": "blocked",
  "auto.runs.outcomeSkipped": "skipped",
  "auto.runs.outcomeQueued": "queued",

  "auto.preview.open": "Preview",
  "auto.preview.title": "Dry-run blast radius",
  "auto.preview.window": "Window",
  "auto.preview.window7": "7d",
  "auto.preview.window30": "30d",
  "auto.preview.window90": "90d",
  "auto.preview.matchesNow": "Matches now: {n}",
  "auto.preview.wouldFire": "Would fire: ~{n} / {days}d",
  "auto.preview.notComputable": "Trailing estimate not computable",
  "auto.preview.hidden": "{n} hidden — no access",
  "auto.preview.explainer":
    "A read-only dry run — no records are changed and nothing is sent.",

  "strength.title": "Relationship strength",
  "strength.score": "Score {score}/100",
  "strength.bucket.dormant": "Dormant",
  "strength.bucket.weak": "Weak",
  "strength.bucket.warm": "Warm",
  "strength.bucket.strong": "Strong",
  "strength.factor.recency": "Recency",
  "strength.factor.frequency": "Frequency",
  "strength.factor.reciprocity": "Reciprocity",
  "strength.factor.direction": "Direction",
  "strength.lastInteraction": "Last interaction: {when}",
  "strength.none": "No interactions yet",
  "strength.inout": "{in} in · {out} out (90d)",
  "strength.computedFrom": "Computed from {count} activities",

  // The relationship-graph cards (ADR-0078). The colleague bands are PO-F-3b's
  // own vocabulary and deliberately differ from the workspace-wide card's:
  // the two measure different things and must not read as comparable.
  "network.title": "Ai bên mình quen họ",
  "network.empty": "Chưa ai bên mình ghi nhận liên hệ với người này.",
  "network.interactions": "{count} lượt tương tác (90 ngày)",
  "network.neverSpoken": "Chưa ghi nhận liên hệ",
  "network.bucket.none": "Chưa liên hệ",
  "network.bucket.weak": "Yếu",
  "network.bucket.moderate": "Vừa",
  "network.bucket.strong": "Mạnh",
  "coverage.title": "Độ phủ",
  "coverage.clear":
    "Không có gì đáng lưu ý — deal này qua mọi kiểm tra độ phủ.",
  "coverage.daysSinceTouch": "{days} ngày",
  "coverage.risk.single_threaded_theirs": "Chỉ một đầu mối",
  "coverage.risk.single_threaded_ours": "Chỉ một đồng nghiệp phụ trách",
  "coverage.risk.coverage_gap": "Không có người ủng hộ đang tham gia",
  "coverage.risk.champion_left": "Người ủng hộ đã rời đi",
  "coverage.risk.stakeholder_left": "Bên liên quan đã rời đi",
  "coverage.risk.going_cold": "Đang nguội dần",

  "cf.title": "Custom fields",
  "cf.formSection": "Custom fields",
  "cf.subtitle":
    "Add a simple typed field to an object you already have — at runtime, no developer, no deploy. New objects and relationships still go through code.",
  "cf.object": "Object",
  "cf.obj.deal": "Deal",
  "cf.obj.organization": "Company",
  "cf.obj.person": "Contact",
  "cf.obj.lead": "Lead",
  "cf.onObject": "Custom fields on {object}",
  "cf.coreExcluded": "Core fields are not shown — they aren't editable here",
  "cf.col.field": "Field",
  "cf.col.type": "Type",
  "cf.col.addedBy": "Added by",
  "cf.addedByYou": "You",
  "cf.addedByAdmin": "Admin",
  "cf.empty.deal":
    "No custom fields on Deal yet. Add one below if you track something we didn't ship.",
  "cf.empty.organization":
    "No custom fields on Company yet. Add one below if you track something we didn't ship.",
  "cf.empty.person":
    "No custom fields on Contact yet. Core fields cover the contact record; add one below if you track more.",
  "cf.empty.lead":
    "No custom fields on Lead yet. A field you add here also appears once a lead is promoted to a contact.",
  "cf.type.text": "Text",
  "cf.type.number": "Number",
  "cf.type.date": "Date",
  "cf.type.currency": "Currency",
  "cf.type.picklist": "Picklist",
  "cf.type.boolean": "Yes / No",
  "cf.builder.addTo": "Add a field to {object}",
  "cf.builder.noCode": "no code",
  "cf.builder.intro":
    "A new field is a real column on the existing table — it filters, reports, exports, and is in the API like any core field. It is not a new object.",
  "cf.label": "Label",
  "cf.apiKey": "API key",
  "cf.apiKeyHint":
    "Auto-derived, immutable once live. Prefixed cf_ so it never collides with a core field.",
  "cf.typeLabel": "Type",
  "cf.currencyCode": "Currency code",
  "cf.currencyHint":
    "Three-letter ISO-4217 code (e.g. EUR, USD). Money is stored to the cent.",
  "cf.options": "Options",
  "cf.addOption": "Add option",
  "cf.removeOption": "Remove option",
  "cf.optionPlaceholder": "Option label",
  "cf.lastOptionBlocked": "A picklist needs at least one option",
  "cf.gate.title": "Adding a field is gated.",
  "cf.gate.body":
    "On confirm it becomes a live column on every {object} — on the 360, in search & filters, lists, export, and the API. The add is written to the audit trail.",
  "cf.refuse.title":
    "That looks like a new object or relationship, not a field.",
  "cf.refuse.body":
    "This builder adds simple fields to existing records only. A new object, a link between objects, or a calculated roll-up is a structural change — it ships as a reviewed change to Margince in a new version, done by people, not by the product editing its own code.",
  "cf.refuse.route":
    "Route it through the development path — your own engineers, an implementation partner, or Gradion services.",
  "cf.confirm": "Confirm & add field",
  "cf.reset": "Reset",
  "cf.writing": "writing…",
  "cf.added": 'Field "{label}" added — live on 360, filters, export & API',
  "cf.edit": "Edit label",
  "cf.archive": "Archive field",
  "cf.archived":
    '"{label}" archived — hidden from new records, retained in audit & history (reversible)',
  "cf.renamePrompt": "New label",
  "cf.renamed": 'Renamed to "{label}"',
  "cf.audit.title": "Recent field changes",
  "cf.audit.empty": "No custom-field changes yet.",
  "cf.audit.loading": "Loading recent changes…",
  "cf.audit.error": "Could not load recent changes — retry shortly.",
  "cf.audit.footer":
    "Every add / edit / archive is recorded permanently in the audit log.",
  "cf.noPermission":
    "You have read-only access to custom fields — the builder and the edit and archive controls are disabled.",
  "cf.retired": "Retired",
  "cf.propagate.title": "Where a new field shows up",
  "cf.propagate.360": "On the record 360 view",
  "cf.propagate.filters": "In search & filters",
  "cf.propagate.list": "As a list / report column",
  "cf.propagate.export": "In CSV export",
  "cf.propagate.api": "On the public REST / MCP API",
  "nav.customFields": "Trường tuỳ chỉnh",
  "settings.customFields": "Trường tuỳ chỉnh",
  "settings.customFieldsSub":
    "Thêm một trường có kiểu vào đối tượng lõi — không cần code, không cần triển khai.",
  "settings.openCustomFields": "Mở trường tuỳ chỉnh",
  "settings.navAria": "Các mục cài đặt",
  "settings.tab.account": "Tài khoản",
  "settings.tab.company": "Bối cảnh công ty",
  "settings.tab.ai": "AI & tự chủ",
  "settings.tab.data": "Mô hình dữ liệu",
  "settings.tab.catalog": "Danh mục",
  "settings.tab.rates": "Tỷ giá & chi phí",
  "settings.tab.privacy": "Quyền riêng tư & chấp thuận",
  "settings.tab.audit": "Nhật ký kiểm toán",
  "settings.tab.voice": "Voice DNA",
  "settings.tab.integrations": "Tích hợp",
  "settings.tab.overlay": "Overlay",
  "settings.group.you": "Cài đặt của bạn",
  "settings.group.org": "Tổ chức",
  "settings.rates.fxTitle": "Tỷ giá",
  "settings.rates.fxIntro":
    "Tỷ giá quy đổi số tiền ngoại tệ về tiền tệ gốc của bạn. Tỷ giá mới có hiệu lực từ hôm nay trở đi; tỷ giá quá khứ không bao giờ bị sửa.",
  "settings.rates.fxAdd": "Đặt tỷ giá",
  "settings.rates.fxEmpty": "Chưa có tỷ giá nào.",
  "settings.rates.fxModalTitle": "Đặt một tỷ giá",
  "settings.rates.rateToBase": "Tỷ giá (về tiền tệ gốc)",
  "settings.rates.modelTitle": "Chi phí mô hình AI",
  "settings.rates.modelIntro":
    "Giá theo từng mô hình, tính bằng USD cho mỗi 1M token, dùng để ước lượng chi tiêu AI. Chỉ để minh bạch — giá không bao giờ làm đổi cách định tuyến mô hình.",
  "settings.rates.modelAdd": "Thêm giá mô hình",
  "settings.rates.modelEmpty": "Chưa có giá mô hình nào.",
  "settings.rates.modelModalTitle": "Đặt giá một mô hình",
  "settings.rates.setRate": "Lưu",
  "settings.rates.refresh": "Làm mới từ nguồn",
  "settings.rates.refreshEnqueued":
    "Đã yêu cầu làm mới — mọi thay đổi được đề xuất sẽ hiện trong hộp phê duyệt.",
  "settings.rates.colFrom": "Từ",
  "settings.rates.colRate": "Tỷ giá (→{base})",
  "settings.rates.colEffective": "Hiệu lực từ",
  "settings.rates.colProvider": "Nhà cung cấp",
  "settings.rates.colModel": "Mô hình",
  "settings.rates.colInput": "Đầu vào $/M",
  "settings.rates.colOutput": "Đầu ra $/M",
  "settings.rates.colCacheRead": "Đọc cache $/M",
  "settings.rates.colCacheWrite": "Ghi cache $/M",
  "settings.voice.title": "Voice DNA",
  "settings.voice.intro":
    "Giọng văn riêng của bạn. Nó định hình các bản nháp viết cho bạn, chỉ riêng bạn thấy, và chỉ học từ những nguồn bạn thêm vào.",
  "settings.voice.emptyTitle": "Chưa có Voice DNA",
  "settings.voice.emptyBody":
    "Hãy thêm vài mẫu văn bên dưới rồi dựng Voice DNA của bạn — hoặc làm việc đó trong Onboarding.",
  "settings.voice.status.collecting": "Đang thu thập",
  "settings.voice.status.ready": "Sẵn sàng",
  "settings.voice.status.stale": "Cần dựng lại",
  "settings.voice.bandThin": "mỏng",
  "settings.voice.bandGood": "đủ",
  "settings.voice.bandRich": "dày",
  "settings.voice.bandSharp": "sắc",
  "settings.voice.version": "phiên bản {n}",
  "settings.voice.derivedLabel": "Giọng văn rút ra được",
  "settings.voice.derivedEmpty":
    "Chưa dựng — hãy thêm mẫu văn rồi dựng để xem giọng văn rút ra được.",
  "settings.voice.personalityLabel": "Tuỳ chọn của bạn",
  "settings.voice.personalityPlaceholder":
    "Ghi chú về giọng văn bạn muốn — giữ nguyên đúng như bạn viết; mô hình không bao giờ ghi đè phần này.",
  "settings.voice.savePreferences": "Lưu tuỳ chọn",
  "settings.voice.corpusLabel": "Mẫu văn",
  "settings.voice.meter": "{count} trên {target} từ",
  "settings.voice.register.email": "email",
  "settings.voice.register.social": "mạng xã hội",
  "settings.voice.register.long_form": "văn dài",
  "settings.voice.register.spoken": "văn nói",
  "settings.voice.register.general": "chung",
  "settings.voice.bandDrop":
    "Gỡ mục này sẽ hạ giọng văn của bạn từ {from} xuống {to}. Hãy bấm gỡ lần nữa để xác nhận.",
  "voice.insights.avoidLabel": "What your voice avoids",
  "voice.insights.voiceScore": "voice match {pct}%",
  "voice.insights.next.addTranscript":
    "Add a call or meeting transcript \u2014 spoken words are your highest-signal source.",
  "voice.insights.next.addEmail":
    "Add sent emails \u2014 they are the primary source for how you write at work.",
  "voice.insights.next.addWords":
    "Add roughly {count} more words to reach the sharp band.",
  "voice.insights.next.atTarget":
    "Your corpus is at target; keep it fresh by adding recent writing occasionally.",
  "voice.status.active": "active",
  "voice.status.candidate": "awaiting review",
  "voice.status.superseded": "superseded",
  "voice.status.rejected": "rejected",
  "voice.classification.routine": "routine change",
  "voice.classification.material": "material change",
  "voice.outcome.autoActivated": "activated automatically",
  "voice.outcome.reviewRequired": "review required",
  "voice.outcome.manuallyActivated": "activated by you",
  "voice.outcome.rejected": "rejected",
  "voice.outcome.rollback": "restored",
  "voice.history.versionRow": "v{n} \u00b7",
  "voice.history.loadMore": "Show older entries",
  "voice.insights.provenance": "Built from your corpus \u00b7 v{n}",
  "voice.insights.statWords": "Words: {count}",
  "voice.insights.statSources": "Sources: {count}",
  "voice.insights.statSentence": "\u2248{count} words per sentence",
  "voice.insights.thinkingLabel": "How you think",
  "voice.insights.movesLabel": "Your signature moves \u2014 in your own words",
  "voice.insights.samplesLabel": "Sample drafts in your voice",
  "voice.insights.draftOnly": "draft only \u2014 never sent",
  "voice.insights.disclosure":
    "AI-assisted drafts; every send stays a human decision.",
  "voice.insights.nextBestLabel": "To make it better:",
  "voice.candidate.title":
    "A new voice version (v{n}) is waiting for your review.",
  "voice.candidate.apply": "Use this version",
  "voice.candidate.reject": "Keep my current voice",
  "voice.history.label": "Versions and learning",
  "voice.history.empty": "No versions yet \u2014 build your voice first.",
  "voice.history.deltasLabel": "What changed",
  "voice.history.deltaRow": "v{from} \u2192 v{to}",
  "voice.history.learning":
    "Learning continuously \u2014 drafts served: {drafted} \u00b7 edited before sending: {edited} \u00b7 rejected: {rejected}.",
  "voice.history.rollback": "Restore version {n}",
  "settings.voice.corpusEmpty": "Chưa có mẫu văn nào.",
  "settings.voice.excluded": "đã loại trừ",
  "settings.voice.removeSource": "Gỡ mẫu văn",
  "settings.voice.pastedLabel": "Văn bản đã dán",
  "settings.voice.addPlaceholder":
    "Dán một email, bài đăng, hay bất cứ gì bạn đã viết…",
  "settings.voice.addSource": "Thêm mẫu văn",
  "settings.voice.addFirstLabel": "Mẫu văn đầu tiên của bạn",
  "settings.voice.addFirstCta": "Thêm và bắt đầu Voice DNA của tôi",
  "settings.voice.building": "Đang dựng…",
  "settings.voice.rebuild": "Dựng lại Voice DNA",
  "settings.voice.buildNeedsWords":
    "Thêm khoảng {n} từ nữa là tôi dựng được giọng văn đầu tiên của bạn. Dưới mức đó thì chưa đủ chữ của bạn để học ra điều gì trung thực.",
  "settings.voice.buildProvisional":
    "Đã đủ để dựng. Thêm khoảng {n} từ nữa sẽ cho bản dựng một hình dung đầy đủ hơn về cách bạn viết.",
  "settings.voice.buildStatus.succeeded": "Đã cập nhật Voice DNA.",
  "settings.voice.buildStatus.failed": "Bản dựng chưa hoàn tất — hãy thử lại.",
  "settings.voice.buildStatus.deferred":
    "Đã xếp hàng — sẽ xong trong chốc lát và tự cập nhật.",
  "settings.voice.buildStatus.pending":
    "Vẫn đang dựng — việc này có thể mất một lát; xong sẽ tự cập nhật ở đây.",
  "settings.tab.users": "Người dùng & vai trò",
  "users.title": "Người dùng & vai trò",
  "users.sub":
    "Mời thành viên, đặt vai trò và vô hiệu hoá quyền truy cập. Chỉ quản trị viên.",
  "users.empty": "Chưa có thành viên nào.",
  "users.adminOnly": "Chỉ quản trị viên mới quản lý được thành viên.",
  "users.emailLabel": "Email của thành viên mới",
  "users.nameLabel": "Họ tên thành viên mới",
  "users.emailPlaceholder": "name@company.com",
  "users.namePlaceholder": "Họ và tên",
  "users.deactivateConfirmTitle": "Vô hiệu hoá {name}?",
  "users.deactivateConfirmBody":
    "Người đó sẽ bị đăng xuất ở mọi nơi và mọi passport Agent của họ bị thu hồi ngay. Bạn có thể kích hoạt lại sau, nhưng họ sẽ phải đăng nhập lại.",
  "users.roleLabel": "Vai trò cho thành viên mới",
  "users.invite": "Mời",
  "users.setRole": "Đặt vai trò…",
  "users.setRoleFor": "Đặt vai trò cho {name}",
  "users.deactivate": "Vô hiệu hoá",
  "users.reactivate": "Kích hoạt lại",
  "users.status.active": "Đang hoạt động",
  "users.status.deactivated": "Đã vô hiệu hoá",
  "users.status.suspended": "Đã tạm khoá",
  "users.role.admin": "Quản trị",
  "users.role.manager": "Quản lý",
  "users.role.rep": "Nhân viên kinh doanh",
  "users.role.read_only": "Chỉ đọc",
  "users.role.ops": "Vận hành",
  "users.link.action": "Lấy liên kết đặt mật khẩu",
  "users.link.title": "Liên kết đặt mật khẩu cho {name}",
  "users.link.pending": "Đang tạo liên kết…",
  // Two sentences, no dash (VOICE-RULE-5).
  "users.link.body":
    "Hãy gửi liên kết này cho thành viên qua một kênh bạn tin tưởng. Nó chỉ dùng được một lần và chỉ hiện lúc này. Đóng đi rồi bạn vẫn tạo được liên kết mới từ dòng của họ.",
  "users.link.urlLabel": "Liên kết đặt mật khẩu",
  "users.link.copy": "Sao chép liên kết",
  "users.link.copied": "Đã sao chép",
  "users.link.copyFailed":
    "Không tự sao chép được. Hãy bôi đen liên kết rồi sao chép.",
  "users.link.expires": "Hết hạn {when}.",
  "users.link.failed":
    "Đã tạo được thành viên nhưng chưa tạo được liên kết. Họ chưa đăng nhập được cho đến khi bạn gửi cho họ một liên kết.",
  "users.link.offline":
    "Không kết nối được tới máy chủ. Hãy kiểm tra kết nối rồi thử lại.",
  "users.link.retry": "Thử lại",
  "users.link.done": "Xong",
  "settings.companyKicker": "Tri thức công ty",
  "settings.companyTitle": "Những gì Margince biết về công ty bạn",
  "settings.companySub":
    "Giữ cho bối cảnh kinh doanh chung — nền cho soạn nháp, báo giá, tìm kiếm và Agent có kiểm soát — luôn chính xác. Mỗi nhận định đều gắn với ai đã cung cấp và nguồn từ đâu.",
  "settings.companyTrust":
    "Chỉ tri thức đã xác nhận — văn bản website không bao giờ trở thành chỉ dẫn.",
  "settings.companyConfirmed": "nhận định đã xác nhận",
  "settings.companyWebsite": "Website công khai của công ty",
  "settings.companyWebsiteRequired":
    "Hãy thêm website công ty trước khi làm mới.",
  "settings.companyRefresh": "Làm mới từ website",
  "settings.companyEssentials": "Ba điều cốt lõi",
  "settings.companyPositioning": "Định vị, người mua và cách bán hàng",
  "settings.companyIdentity": "Danh tính và thông tin pháp lý",
  "settings.companyViewSource": "Xem nguồn",
  "settings.companySave": "Lưu bối cảnh công ty",
  "settings.companySaved": "Đã lưu",
  "settings.companyRefreshUnavailable":
    "Lần làm mới từ website này không còn nữa.",
  "settings.companyRefreshStale":
    "Đề xuất từ website đã đổi. Hãy xem lại bản so sánh mới trước khi áp dụng.",
  "settings.companyRefreshReview": "So sánh với website",
  "settings.companyRefreshReady": "Xem những gì đã đổi",
  "settings.companyRefreshReading": "Đang đọc và đối chiếu website của bạn…",
  "settings.companyCoverage": "độ phủ trang",
  "settings.companyResolveAll":
    "Hãy chọn kết quả cho mọi xung đột với giá trị do người đặt.",
  "settings.companyApplyRefresh": "Áp dụng thay đổi đã chọn",
  "settings.companySelectChange": "Chọn thay đổi cho {field}",
  "settings.companyCurrent": "Giá trị đã xác nhận hiện tại",
  "settings.companyWebsiteProposal": "Đề xuất từ website",
  "settings.companyClass.new": "Mới",
  "settings.companyClass.machine_change": "Website đã đổi",
  "settings.companyClass.human_conflict": "Cần bạn quyết định",
  "settings.companyClass.unchanged": "Không đổi",
  "settings.companyResolution.keep_current": "Giữ hiện tại",
  "settings.companyResolution.accept_proposal": "Nhận theo website",
  "settings.companyResolution.use_value": "Dùng giá trị tôi đã sửa",
  "settings.companyManualKicker": "Thiết lập thủ công, riêng tư",
  "settings.companyManualTitle": "Cho Margince biết những điều cốt lõi",
  "settings.companyManualSub":
    "Bản triển khai này không bật việc đọc website. Ba câu trả lời sau đã đủ để tạo bối cảnh công ty hữu ích, không gọi mô hình và không có yêu cầu ra bên ngoài.",
  "settings.companyCreateWorkspace": "Tạo bối cảnh công ty",
  "product.title": "Sản phẩm",
  "product.settingsSub":
    "Các mục bảng giá mà dòng báo giá chụp lại giá trị từ đó.",
  "product.open": "Mở sản phẩm",
  "product.new": "Sản phẩm mới",
  "product.edit": "Sửa sản phẩm",
  "product.archive": "Lưu trữ sản phẩm",
  "product.archiveConfirm":
    "Lưu trữ sản phẩm này? Các dòng báo giá hiện có vẫn giữ bản chụp giá của chúng.",
  "product.name": "Tên",
  "product.sku": "SKU",
  "product.description": "Mô tả",
  "product.unit": "Đơn vị",
  "product.unitPrice": "Đơn giá",
  "product.currency": "Tiền tệ",
  "product.taxRate": "Thuế suất mặc định %",
  "product.active": "Đang bán",
  "product.activeFilter": "Chỉ đang bán",
  "product.inactive": "Ngừng bán",
  "product.archived": "Đã lưu trữ",
  "product.sortName": "Tên",
  "product.sortCreated": "Mới nhất",
  "product.empty": "Chưa có sản phẩm nào.",

  "template.title": "Offer templates",
  "template.settingsSub": "Branded DE/EN PDF layouts for offers.",
  "template.open": "Open offer templates",
  "template.new": "New template",
  "template.edit": "Edit template",
  "template.archive": "Archive template",
  "template.archiveConfirm":
    "Archive this template? Offers that reference it fall back to the locale default.",
  "template.name": "Name",
  "template.locale": "Locale",
  "template.isDefault": "Default for locale",
  "template.header": "Header text",
  "template.footer": "Footer text",
  "template.localeFilter": "Locale",
  "template.localeDE": "German (DE)",
  "template.localeEN": "English (US)",
  "template.sortName": "Name",
  "template.empty": "No offer templates yet.",

  "tools.title": "Công cụ Agent",
  "tools.sub":
    "Bề mặt có kiểm soát mà một passport gọi được — đúng danh sách mà một client MCP thấy.",
  "tools.col.tool": "Công cụ",
  "tools.col.verb": "Thao tác",
  "tools.col.scope": "Phạm vi",
  "tools.col.tier": "Bậc",
  "tools.col.egress": "Ra ngoài",
  "tools.egress": "có gọi ra ngoài",
  "tools.scopeAll": "Mọi passport",
  "tools.scopedTo": "{label} gọi được",
  "tools.unreachable": "chưa được cấp phạm vi",

  "aiusage.title": "AI usage & budget",
  "aiusage.sub":
    "Your own bill, made visible — per task and tier, token-denominated.",
  "aiusage.budget": "{spent} of {budget} tokens · {pct}%",
  "aiusage.band.normal": "normal",
  "aiusage.band.degraded": "economy mode",
  "aiusage.band.queued": "budget reached — background AI queued",
  "aiusage.band.unknown": "unknown budget state",
  "aiusage.col.task": "Task",
  "aiusage.col.tier": "Tier",
  "aiusage.col.calls": "Calls",
  "aiusage.col.cached": "Cached",
  "aiusage.col.tokensIn": "Tokens in",
  "aiusage.col.tokensOut": "Tokens out",
  "aiusage.col.cost": "Est. cost",
  "aiusage.costNote": "Costs are estimates at configured rates.",
  "aiusage.days.show": "Show days",
  "aiusage.days.hide": "Hide days",
  "aiusage.empty": "No AI calls in this window.",
  "aiusage.prevMonth": "Previous month",
  "aiusage.nextMonth": "Next month",

  "aibanner.degraded": "AI running in economy mode.",
  "aibanner.queued": "AI budget reached — background AI is queued.",
  "aibanner.unknown": "AI budget status is not recognized.",
  "aibanner.link": "View usage",
  "aibanner.dismiss": "Dismiss",

  "aicalls.title": "AI call trace",
  "aicalls.sub":
    "Every model call — routing identity, tokens, retries, captured payload.",
  "aicalls.col.when": "When",
  "aicalls.col.task": "Task",
  "aicalls.col.model": "Model",
  "aicalls.col.tokens": "Tokens",
  "aicalls.col.latency": "Latency",
  "aicalls.ms": "{value} ms",
  "aicalls.badge.cacheHit": "cache hit",
  "aicalls.badge.degraded": "degraded",
  "aicalls.badge.retries": "retry ×{count}",
  "aicalls.filter.all": "All tasks",
  "aicalls.loadMore": "Load more",
  "aicalls.empty": "No AI calls recorded yet.",
  "aicalls.detail.identity":
    "Served {served} via {provider} (configured: {configured})",
  "aicalls.detail.source": "Served identity source: {source}",
  "aicalls.detail.context": "Injected context: {scopes}",
  "aicalls.detail.contextNone": "No company context injected",
  "aicalls.detail.attempts": "Attempts",
  "aicalls.detail.request": "Request payload",
  "aicalls.detail.response": "Response payload",
  "aicalls.payload.off":
    "Payload capture is off — set ai.capture_payloads: true in margince.yaml to record request/response content.",
  "aicalls.payload.none": "No payload captured for this call.",

  "aiexport.button": "Export as cert scenario",
  "aiexport.title": "Export run as certification scenario",
  "aiexport.nameLabel": "Scenario name",
  "aiexport.checklist":
    "Secrets were stripped at capture. Personal data was NOT — review and remove PII, then replace sanitized_by before committing this file to the corpus.",
  "aiexport.copy": "Copy YAML",
  "aiexport.copied": "Copied",
  "aiexport.download": "Download .yaml",
  "aiexport.copyFailed": "Copy failed — use the preview or download instead.",
  "aiexport.close": "Close",
  "aiexport.previewLabel": "Scenario preview",
  "aiexport.responseLabel": "Model response",

  "countdown.minutesSeconds": "{minutes}m {seconds}s",
  "countdown.expired": "Expired",

  // Quotas & attainment (RD-T06): human-set revenue targets with
  // server-computed attainment, surfaced under the Reports "Quotas" segment.
  "quotas.tab": "Chỉ tiêu",
  "quotas.sub": "mục tiêu doanh thu — do người đặt, mức đạt do hệ thống tính",
  "quotas.role.owner": "Chỉ tiêu cá nhân",
  "quotas.role.team": "Chỉ tiêu nhóm",
  "quotas.periodRange": "{start} – {end}",
  "quotas.empty.title": "Chưa đặt chỉ tiêu",
  "quotas.empty.body":
    "Chỉ tiêu là mục tiêu do con người đặt — người phụ trách hay nhóm, kỳ, số tiền. Hệ thống không đoán thay bạn. Hãy đặt một mục tiêu để bắt đầu theo dõi mức đạt từ các deal đã thắng.",
  "quotas.empty.cta": "Đặt mục tiêu",
  "quotas.attained": "đã đạt",
  "quotas.closedWon": "Đã thắng trong kỳ này",
  "quotas.target": "Mục tiêu",
  "quotas.gap": "Khoảng cách tới mục tiêu",
  "quotas.baseCurrencyNote":
    "Số liệu tính theo tiền tệ gốc của workspace ({currency}).",
  "quotas.pace.ahead":
    "Vượt tiến độ — đã đạt {pct}% trong khi kỳ đã trôi {pace}%.",
  "quotas.pace.behind":
    "Chậm tiến độ — đã đạt {pct}% trong khi kỳ đã trôi {pace}%.",
  "quotas.pace.met": "Đã đạt mục tiêu — {pct}%.",
  "quotas.computed": "tính ở phía máy chủ",
  "quotas.contributing.title": "Những gì được tính vào mức đạt",
  "quotas.contributing.subtitle":
    "deal đã thắng · giá trị quy về tiền tệ gốc trong kỳ",
  "quotas.contributing.deal": "Deal",
  "quotas.contributing.amount": "Số tiền được tính",
  "quotas.contributing.total": "Tổng được tính",
  "quotas.contributing.caption":
    "Tiền tệ gốc · không tính deal đang mở / đã thua / bị loại trừ",
  "quotas.explain.formula":
    "mức đạt = Σ(giá trị gốc của deal đã thắng) ÷ mục tiêu, chính xác đến từng xu",
  "quotas.explain.closedWon": "đã thắng = {sum} ({count} deal trong kỳ)",
  "quotas.explain.target": "mục tiêu = {target} (do người đặt)",
  "quotas.explain.result": "mức đạt = {sum} ÷ {target} = {pct}%",
  "quotas.explain.exclusions":
    "không tính deal đang mở / đã thua / bị loại trừ; chỉ dùng phần lõi sạch",
  "quotas.scopeNote.title": "Chỉ tiêu này cố ý là gì",
  "quotas.scopeNote.flag": "nêu rõ, không giấu",
  "quotas.scopeNote.body":
    "Mục tiêu do con người đặt — AI không bao giờ tự nghĩ ra con số chỉ tiêu. Mức đạt được tính từ giá trị gốc của các deal đã thắng và kiểm toán được đầy đủ. Chưa có mục tiêu do AI đặt, chưa có việc tự điền dự báo vào chỉ tiêu, và cũng chưa có bộ máy tính lương thưởng hay hoa hồng.",
  "quotas.target.title": "Mục tiêu của kỳ",
  "quotas.target.new": "Đặt mục tiêu",
  "quotas.target.edit": "Sửa mục tiêu",
  "quotas.target.save": "Lưu mục tiêu",
  "quotas.target.note":
    "Việc sửa ghi lại một giá trị do người nhập và ghi nhận thay đổi. Mức đạt sẽ được tính lại theo giá trị mới.",
  "quotas.target.sideFixed":
    "Chỉ tiêu đã gắn cho cá nhân hay nhóm thì không đổi được — muốn đổi thì lưu trữ rồi tạo lại.",
  "quotas.side.label": "Giao cho",
  "quotas.side.owner": "Người phụ trách",
  "quotas.side.team": "Nhóm",
  "quotas.owner": "Người phụ trách",
  "quotas.team": "Nhóm",
  "quotas.pickOwner": "Chọn người phụ trách…",
  "quotas.pickTeam": "Chọn nhóm…",
  "quotas.amountHint": "Số euro chẵn — không có phần thập phân",
  "quotas.periodStart": "Bắt đầu kỳ",
  "quotas.periodEnd": "Kết thúc kỳ",
  "quotas.amount": "Số tiền mục tiêu",
  "quotas.currency": "Tiền tệ",
  "quotas.err.targetZero": "Chỉ tiêu này chưa có mục tiêu",
  "quotas.err.computeFailed": "Không tính được mức đạt",
  "quotas.err.ownerXorTeam": "Hãy chọn đúng một: người phụ trách hoặc nhóm.",
  "quotas.archive.title": "Lưu trữ chỉ tiêu",
  "quotas.archive.confirm":
    "Lưu trữ sẽ đưa chỉ tiêu này ra khỏi danh sách và ngừng theo dõi mức đạt. Chỉ tiêu đã lưu trữ thì không sửa được.",

  "captureSettings.title": "Capture",
  "captureSettings.sub":
    "How captured companies and contacts are enriched after they are created.",
  "captureSettings.autoEnrich.label": "Auto-enrich captured companies",
  "captureSettings.autoEnrich.help":
    "When on, each new company created from captured mail gets an automatic web dossier — its site is read and its profile filled in. Runs under a daily limit.",
  "captureSettings.adminOnly": "Only an admin or ops can change this.",

  "webhooks.title": "Webhook",
  "webhooks.sub":
    "Các đăng ký gửi ra, nhận HTTP POST có chữ ký cho những sự kiện được chọn.",
  "webhooks.new": "Đăng ký mới",
  "webhooks.notConfigured":
    "Webhook gửi ra chưa được bật trên bản triển khai này — cần cấu hình khoá ký trước.",
  "webhooks.state.active": "Đang bật",
  "webhooks.state.paused": "Tạm dừng",
  "webhooks.updated": "Cập nhật {date}",
  "webhooks.field.targetUrl": "URL đích",
  "webhooks.field.eventTypes": "Loại sự kiện",
  "webhooks.field.state": "Trạng thái",
  "webhooks.edit": "Sửa",
  "webhooks.archive": "Lưu trữ",
  "webhooks.archiveConfirm":
    "Lưu trữ sẽ dừng mọi lượt gửi cho đăng ký này. Không thể hoàn tác.",
  "webhooks.rotate": "Đổi khoá ký",
  "webhooks.rotateConfirm.title": "Đổi khoá ký?",
  "webhooks.rotateConfirm.body":
    "Xác nhận sẽ vô hiệu hoá khoá hiện tại ngay lập tức rồi hiện khoá mới đúng một lần. Hãy sao chép và cập nhật cho bên nhận ngay khi đổi xong.",
  "webhooks.secret.title": "Khoá ký",
  "webhooks.secret.warning":
    "Khoá này chỉ hiện một lần và không lấy lại được. Hãy lưu ngay — mọi lượt gửi đều được ký bằng nó.",
  "webhooks.secret.copy": "Sao chép",
  "webhooks.secret.copied": "Đã sao chép",
  "webhooks.secret.copyFailed":
    "Không tự sao chép được — hãy bôi đen rồi tự sao chép khoá.",
  "webhooks.secret.done": "Xong",

  "webhooks.deliveries.show": "Xem lượt gửi",
  "webhooks.deliveries.hide": "Ẩn lượt gửi",
  "webhooks.deliveries.empty": "Chưa có lượt gửi nào.",
  "webhooks.deliveries.deadLetterGroup": "Đã bỏ vào hàng lỗi ({count})",
  "webhooks.deliveries.allGroup": "Các lượt khác",
  "webhooks.deliveries.column.status": "Trạng thái",
  "webhooks.deliveries.column.event": "Sự kiện",
  "webhooks.deliveries.column.attempts": "Số lần thử",
  "webhooks.deliveries.column.lastStatusCode": "Trạng thái cuối",
  "webhooks.deliveries.column.lastError": "Lỗi cuối",
  "webhooks.deliveries.column.created": "Tạo lúc",
  "webhooks.deliveries.column.resolved": "Kết thúc / lần thử tới",
  "webhooks.deliveries.status.pending": "Đang chờ",
  "webhooks.deliveries.status.delivered": "Đã gửi",
  "webhooks.deliveries.status.retrying": "Đang thử lại",
  "webhooks.deliveries.status.dead_lettered": "Đã bỏ vào hàng lỗi",
  "webhooks.deliveries.replay": "Gửi lại",
  "webhooks.deliveries.replayConfirm.title": "Gửi lại lượt này?",
  "webhooks.deliveries.replayConfirm.body":
    "Thử gửi lại ngay, ký bằng khoá hiện tại và một dấu thời gian mới. Nó không chờ lần thử theo lịch kế tiếp.",
  "reindexbanner.needed": "Reindex needed",
  "reindexbanner.link": "Review in settings",

  "embedreindex.title": "Search index",
  "embedreindex.sub":
    "The embedding store's reindex status — admin/ops only, including viewing it.",
  "embedreindex.loading": "Checking index status…",
  "embedreindex.statusUnavailable": "Index status is not available right now.",
  "embedreindex.statusIdle": "Up to date",
  "embedreindex.statusNeeded": "Reindex needed",
  "embedreindex.statusReembedding": "Reindexing…",
  "embedreindex.lastProgress": "Last progress {duration} ago",
  "embedreindex.entitiesPending": "{count} entities pending",
  "embedreindex.workspacePending": "{count} pending",
  "embedreindex.reviewCta": "Review & reindex",
  "embedreindex.rebuildCta": "Rebuild index",
  "embedreindex.confirmTitle": "Start the reindex",
  "embedreindex.rebuildTitle": "Rebuild the search index",
  "embedreindex.confirmCta": "Start reindex",
  "embedreindex.rebuildConfirmCta": "Rebuild now",
  "embedreindex.starting": "Starting…",
  "embedreindex.previewLoading": "Estimating scope…",
  "embedreindex.estimateEntities": "Entities to (re)embed:",
  "embedreindex.estimateTokens": "Estimated AI tokens:",
  "embedreindex.estimateCost": "Estimated cost:",
  "embedreindex.estimateQualityHeuristic":
    "Heuristic estimate — a cold work-shape floor, not observed spend.",
  "embedreindex.utilizationTitle": "Per-workspace budget impact",
  "embedreindex.impact.normal": "normal",
  "embedreindex.impact.degraded": "would enter economy mode",
  "embedreindex.impact.queued": "would be queued",

  "consent.title": "Cho phép truy cập",
  "consent.asks":
    "{client} muốn hành động trong Margince với danh nghĩa của bạn.",
  "consent.lend": "Cho nó mượn một passport Agent của bạn",
  "consent.grantedNote":
    "Kết nối này nhận đúng những phạm vi hiển thị ở đây — đúng những gì passport này mang.",
  "consent.offline":
    "Nó sẽ giữ kết nối mà không hỏi lại, tự gia hạn quyền truy cập cho đến khi bạn thu hồi.",
  "consent.approve": "Cho phép",
  "consent.deny": "Từ chối truy cập",
  "consent.emptyTitle": "Bạn cần có một passport Agent trước đã",
  "consent.emptyBody":
    "Passport là quyền hạn bạn cho một Agent mượn — nó không bao giờ vượt quá quyền của chính bạn, và bạn thu hồi được bất cứ lúc nào. Hãy tạo một passport, chúng tôi sẽ đưa bạn quay lại đây để hoàn tất kết nối {client}.",
  "consent.emptyCta": "Tạo một passport",
  "consent.expires": "hết hạn {date}",
  "consent.resumeTitle": "Hoàn tất kết nối {client}",
  "consent.resumeBody":
    "Bạn tới đây để tạo một passport cho {client}. Có rồi thì tiếp tục từ chỗ đang dở.",
  "consent.resume": "Tiếp tục kết nối",
  "consent.resumeDismiss": "Huỷ kết nối này",
  "consent.reentering": "Đang kết nối lại…",
  "consent.backToApp": "Quay lại Margince",
  "consent.staleTitle": "Yêu cầu này đã hết hạn",
  // No {client}: this card renders without the consent-request fetch, so the
  // client's name is not available to name here.
  "consent.staleBody":
    "Yêu cầu kết nối không còn hiệu lực. Hãy quay lại ứng dụng bạn đang kết nối và bắt đầu lại — tải lại trang này không giúp được gì.",
  "consent.unlendableTitle": "Passport đó không cho mượn được nữa",
  "consent.unlendableBody":
    "Passport bạn chọn cho {client} đã bị thu hồi, đã hết hạn, hoặc đang gắn với một kết nối khác. Hãy chọn passport khác bên dưới.",
  "consent.invalidTitle": "Không hoàn tất được yêu cầu kết nối này",
  "consent.invalidBody":
    "Bản cài đặt này sẽ không cho phép yêu cầu ở dạng hiện tại — có thể ứng dụng không còn được đăng ký ở đây. Hãy quay lại ứng dụng bạn đang kết nối và bắt đầu lại.",
  "consent.unnamedPassport": "Passport chưa đặt tên ({id})",
  "person.thin.title": "Những gì đã biết đến giờ",
  "person.thin.known":
    "Đã có {what} của {name}, nhưng chưa ai bên mình ghi nhận trao đổi với họ.",
  "person.thin.remediation.capture":
    "Hãy kết nối hộp thư vẫn viết cho họ, trang này sẽ tự đầy lên — mỗi trường kèm nguồn gốc của nó.",
  "person.thin.remediation.employer":
    "Hãy thêm nơi họ làm việc, Margince sẽ đọc website công ty đó để tìm vai trò của họ.",
  "person.thin.logFirst": "Ghi nhận tương tác đầu tiên",
  "person.timeline.all": "Tất cả",
  "person.timeline.messages": "Tin nhắn",
  "person.timeline.meetings": "Cuộc họp",
  "person.timeline.tasks": "Công việc",
  "person.enriched.title": "Những gì Margince đọc được",
  "person.enriched.sub":
    "Mỗi giá trị kèm đoạn văn bản đã đọc ra nó. Bạn sửa một giá trị thì bản sửa được giữ nguyên.",
  "person.enriched.field.title": "Chức danh",
  "person.enriched.field.phone": "Điện thoại",
  "person.enriched.field.role": "Vai trò",
  "person.enriched.field.linkedin": "LinkedIn",
  "person.enriched.field.org_name": "Công ty",
  "person.enriched.readFrom": "Đọc từ {source} vào {when}",
  "person.enriched.correctedByYou": "Bạn đã sửa",
  "person.enriched.confirmed": "Đã xác nhận",
  "person.enriched.correct": "Sửa",
  "person.enriched.confirm": "Đúng rồi",
  "person.enriched.save": "Lưu bản sửa",
  "person.enriched.cancel": "Huỷ",
  "person.graph.loading": "Đang đọc mạng lưới quanh contact này…",
  "person.graph.routeTitle": "Đường vào thân thiết nhất",
  "person.graph.routeDirect": "{name} đã có trao đổi với họ.",
  "person.graph.routeVia": "{name} có trao đổi với {through} ở cùng công ty.",
  "person.graph.noRoute":
    "Chưa ai bên mình trao đổi với họ hay với ai ở công ty họ.",
  "person.graph.direct": "Ai quen họ",
  "person.graph.directSub":
    "Những đồng nghiệp đã tự mình trao đổi với contact này.",
  "person.graph.noDirect": "Chưa ai bên mình trao đổi với họ.",
  "person.graph.account": "Ở cùng công ty",
  "person.graph.accountSub":
    "Đồng nghiệp của họ, và ai bên mình thân nhất với từng người.",
  "person.graph.noAccount":
    "Không có contact nào khác được ghi nhận ở công ty họ.",
  "person.graph.omitted": "Một phần bị ẩn vì bạn không có quyền xem.",
  "person.graph.noEdge": "Chưa ghi nhận trao đổi nào với {name}.",
  "person.graph.withColleague": "với {name}",
  "person.graph.withContact": "với contact này",
  "person.graph.counts":
    "{total} lượt tương tác trong 90 ngày · {inbound} đến, {outbound} đi",
  "person.graph.countsOnly":
    "Chỉ là số đếm — nội dung tin nhắn vẫn nằm trên timeline.",
  "person.graph.untitledMessage": "Tin nhắn không có tiêu đề",
  "person.graph.dropped": "Còn {count} mục không hiển thị.",
  "person.moment.dismiss": "Để sau",
  "person.moment.recommended": "Tiếp theo:",
  "person.moment.willConfirm": "sẽ hỏi bạn xác nhận",
  "person.moment.blocked": "Không dùng được trên bản ghi này.",
  "person.moment.kind.replied_after_gap": "Họ đã quay lại",
  "person.moment.kind.unanswered_inbound": "Bạn còn nợ hồi đáp",
  "person.moment.kind.meeting_ahead": "Sắp tới",
  "person.moment.kind.task_overdue": "Quá hạn",
  "person.moment.kind.went_quiet": "Đã im ắng",
  "person.change.repliedAfterGap": "Họ hồi đáp sau {days} ngày im ắng.",
  "person.change.wentQuiet": "Không có gì xảy ra suốt {days} ngày.",
  "person.change.warmed": "Quan hệ đã chuyển từ {from} lên {to}.",
  "person.change.cooled": "Quan hệ đã tụt từ {from} xuống {to}.",
  "person.band.none": "chưa liên hệ",
  "person.band.weak": "yếu",
  "person.band.moderate": "vừa",
  "person.band.strong": "mạnh",
  "person.pulse.title": "Quan hệ",
  "person.pulse.warmestIs": "{name} có quan hệ thân thiết nhất bên mình.",
  "person.pulse.nobodyYet": "Chưa ai bên mình ghi nhận trao đổi với họ.",
  "person.pulse.lastInbound": "Họ viết lần cuối",
  "person.pulse.lastOutbound": "Mình viết lần cuối",
  "person.pulse.neverInbound": "chưa bao giờ",
  "person.pulse.neverOutbound": "chưa bao giờ",
  "person.pulse.why": "Cách tính con số này",
  "person.pulse.arithmetic":
    "Điểm {score}/100 = 100 x độ gần đây {recency} x tần suất {frequency} x mức qua lại {reciprocity}. Tính lúc đọc từ nhịp trao đổi đã thu thập, không bao giờ lưu lại.",
  "person.identity.title": "Danh tính",
  "person.identity.email": "Email",
  "person.identity.phone": "Điện thoại",
  "person.identity.currentRole": "Vai trò hiện tại",
  "person.identity.buyingRole": "Vai trò mua hàng",
  "person.career.title": "Vai trò trước đây",
  "person.consent.title": "Chốt kiểm gửi ra",
  "person.consent.allowed": "Được phép: {purposes}",
  "person.consent.noneGranted":
    "Chưa mục đích nào được cấp phép, nên việc gửi ra vẫn bị chặn.",
  "person.consent.blocked": "Bị chặn: {purposes}",
  "person.network.title": "Ai bên mình quen họ",
  "person.network.twoWay": "{count} lượt trao đổi hai chiều trong 90 ngày",
  "person.network.oneSided": "{count} lượt tương tác trong 90 ngày, một chiều",
  "person.network.replied": "đã hồi đáp {when}",
} as const satisfies Record<MessageKey, string>;
