import { CircleCheck, CircleX, Info, Minus, RefreshCw, RotateCcw, Search, TriangleAlert } from '@lucide/vue'
import {
  ElAlert,
  ElButton,
  ElDialog,
  ElEmpty,
  ElPagination,
  ElSkeleton,
  ElTable,
  ElTag,
  ElTooltip
} from 'element-plus'
import { defineComponent, h, type PropType } from 'vue'

export const AppPage = defineComponent({
  name: 'AppPage',
  props: {
    title: { type: String, required: true },
    description: String,
    busy: Boolean,
    error: String
  },
  emits: ['retry'],
  setup(props, { emit, slots }) {
    return () => h('section', { class: 'ga-app-page', 'aria-busy': props.busy || undefined }, [
      h('header', { class: 'ga-app-page__header' }, [
        h('div', { class: 'ga-app-page__heading' }, [
          h('h1', { class: 'ga-app-page__title' }, props.title),
          props.description ? h('p', { class: 'ga-app-page__description' }, props.description) : null
        ]),
        slots.actions ? h('div', { class: 'ga-app-page__actions' }, slots.actions()) : null
      ]),
      props.error ? h(ElAlert, { class: 'ga-app-page__error', type: 'error', title: props.error, closable: false, showIcon: true }, {
        default: () => h(ElButton, { link: true, type: 'danger', onClick: () => emit('retry') }, () => '重试')
      }) : null,
      props.busy
        ? h(ElSkeleton, { animated: true, rows: 5 })
        : slots.default?.() ?? h(ElEmpty, { class: 'ga-empty-state', description: '暂无内容' })
    ])
  }
})

export const QueryBar = defineComponent({
  name: 'QueryBar',
  props: { busy: Boolean, resetDisabled: Boolean },
  emits: ['reset', 'search'],
  setup(props, { emit, slots }) {
    const icon = (component: typeof Search) => h(component, { size: 16, 'aria-hidden': 'true' })
    return () => h('form', {
      class: 'ga-query-bar',
      role: 'search',
      onSubmit: (event: Event) => { event.preventDefault(); emit('search') }
    }, [
      h('div', { class: 'ga-query-bar__fields' }, slots.default?.()),
      h('div', { class: 'ga-query-bar__actions' }, [
        slots.actions?.(),
        h(ElButton, { disabled: props.resetDisabled || props.busy, onClick: () => emit('reset') }, () => [icon(RotateCcw), '重置']),
        h(ElButton, { nativeType: 'submit', type: 'primary', loading: props.busy }, () => [icon(Search), '查询'])
      ])
    ])
  }
})

export const TableToolbar = defineComponent({
  name: 'TableToolbar',
  props: { selectedCount: { type: Number, default: 0 }, busy: Boolean },
  emits: ['refresh'],
  setup(props, { emit, slots }) {
    return () => h('div', { class: 'ga-table-toolbar', 'aria-label': '表格工具栏' }, [
      h('div', { class: 'ga-table-toolbar__actions' }, slots.default?.()),
      props.selectedCount > 0 ? h('span', { class: 'ga-table-toolbar__selection' }, `已选择 ${props.selectedCount} 项`) : null,
      h('span', { class: 'ga-table-toolbar__spacer' }),
      slots.end?.(),
      h(ElTooltip, { content: '刷新列表', placement: 'top' }, {
        default: () => h(ElButton, { circle: true, loading: props.busy, 'aria-label': '刷新列表', onClick: () => emit('refresh') }, {
          default: () => props.busy ? null : h(RefreshCw, { size: 16, 'aria-hidden': 'true' })
        })
      })
    ])
  }
})

export const EmptyState = defineComponent({
  name: 'EmptyState',
  props: {
    title: { type: String, default: '暂无数据' },
    actionLabel: String
  },
  emits: ['action'],
  setup(props, { emit, slots }) {
    return () => h(ElEmpty, { class: 'ga-empty-state', description: props.title }, {
      default: () => props.actionLabel
        ? h(ElButton, { type: 'primary', onClick: () => emit('action') }, () => props.actionLabel)
        : slots.default?.()
    })
  }
})

type TableRow = Record<string, unknown>
export const DataTable = defineComponent({
  name: 'DataTable',
  props: {
    rows: { type: Array as PropType<ReadonlyArray<TableRow>>, required: true },
    rowKey: [String, Function] as PropType<string | ((row: TableRow) => string)>,
    loading: Boolean,
    error: String,
    emptyTitle: { type: String, default: '暂无数据' },
    label: { type: String, default: '数据表格' }
  },
  setup(props, { slots }) {
    return () => h('div', {
      class: 'ga-data-table', role: 'region', 'aria-label': props.label, 'aria-busy': props.loading || undefined
    }, props.loading
      ? h('div', { class: 'ga-data-table__state' }, [h(ElSkeleton, { animated: true, rows: 5 })])
      : props.error
        ? h('div', { class: 'ga-data-table__state' }, [h(ElAlert, { type: 'error', title: props.error, closable: false, showIcon: true })])
        : h('div', { class: 'ga-data-table__scroll' }, [
            h(ElTable, { data: [...props.rows], rowKey: props.rowKey }, {
              default: slots.default,
              empty: () => slots.empty?.() ?? props.emptyTitle
            })
          ]))
  }
})

export const FormGrid = defineComponent({
  name: 'FormGrid',
  props: { columns: { type: Number as PropType<1 | 2 | 3>, default: 2 } },
  setup(props, { slots }) {
    return () => h('div', { class: 'ga-form-grid', style: { '--ga-form-columns': String(props.columns) } }, slots.default?.())
  }
})

export const FormDialog = defineComponent({
  name: 'FormDialog',
  props: {
    modelValue: { type: Boolean, required: true },
    title: { type: String, required: true },
    busy: Boolean,
    danger: Boolean,
    submitLabel: { type: String, default: '保存' },
    width: { type: [String, Number], default: 640 }
  },
  emits: ['cancel', 'submit', 'update:modelValue'],
  setup(props, { emit, slots }) {
    return () => h(ElDialog, {
      class: 'ga-form-dialog',
      modelValue: props.modelValue,
      title: props.title,
      width: props.width,
      closeOnClickModal: false,
      closeOnPressEscape: !props.busy,
      closeOnHashChange: false,
      showClose: !props.busy,
      destroyOnClose: true,
      alignCenter: true,
      'onUpdate:modelValue': (value: boolean) => emit('update:modelValue', value),
      onClose: () => emit('cancel')
    }, {
      default: slots.default,
      footer: () => h('div', { class: 'ga-form-dialog__footer' }, [
        h(ElButton, { disabled: props.busy, onClick: () => emit('update:modelValue', false) }, () => '取消'),
        h(ElButton, { type: props.danger ? 'danger' : 'primary', loading: props.busy, onClick: () => emit('submit') }, () => props.submitLabel)
      ])
    })
  }
})

type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral'
export const StatusTag = defineComponent({
  name: 'StatusTag',
  props: {
    tone: { type: String as PropType<StatusTone>, default: 'neutral' },
    label: { type: String, required: true }
  },
  setup(props) {
    const presentations = {
      success: { icon: CircleCheck, type: 'success' as const },
      warning: { icon: TriangleAlert, type: 'warning' as const },
      danger: { icon: CircleX, type: 'danger' as const },
      info: { icon: Info, type: 'info' as const },
      neutral: { icon: Minus, type: 'info' as const }
    }
    return () => {
      const presentation = presentations[props.tone]
      return h(ElTag, { class: 'ga-status-tag', type: presentation.type, effect: 'light', title: props.label }, {
        default: () => [
          h(presentation.icon, { size: 14, 'aria-hidden': 'true' }),
          h('span', { class: 'ga-status-tag__label' }, props.label)
        ]
      })
    }
  }
})

export const Pagination = defineComponent({
  name: 'Pagination',
  props: {
    page: { type: Number, required: true },
    pageSize: { type: Number, required: true },
    total: { type: Number, required: true },
    disabled: Boolean,
    pageSizes: { type: Array as PropType<number[]>, default: () => [10, 20, 50, 100] }
  },
  emits: ['update:page', 'update:pageSize'],
  setup(props, { emit }) {
    return () => h('nav', { class: 'ga-pagination', 'aria-label': '分页' }, [
      h(ElPagination, {
        currentPage: props.page,
        pageSize: props.pageSize,
        pageSizes: props.pageSizes,
        total: props.total,
        disabled: props.disabled,
        layout: 'total, sizes, prev, pager, next, jumper',
        background: true,
        'onUpdate:currentPage': (page: number) => emit('update:page', page),
        'onUpdate:pageSize': (pageSize: number) => emit('update:pageSize', pageSize)
      })
    ])
  }
})
