/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { FileValidationIcon, Robot01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import type { LogOtherData } from '../types'

export function ServiceTierBadge(props: { tier: string }) {
  const { t } = useTranslation()
  const priority = props.tier === 'priority' || props.tier === 'fast'
  let label = props.tier
  if (priority) {
    label = t('Priority')
  } else if (props.tier === 'default' || props.tier === 'standard') {
    label = t('Standard')
  } else if (props.tier === 'auto') {
    label = t('Auto')
  } else if (props.tier === 'flex') {
    label = t('Flex')
  }
  const description = `${t('Service Tier')}: ${label}`
  return (
    <StatusBadge
      label={label}
      title={description}
      variant={priority ? 'amber' : 'neutral'}
      copyable={false}
      className='max-w-full text-xs'
    />
  )
}

export function LogRequestMetadata(props: { other: LogOtherData | null }) {
  const { t } = useTranslation()
  const kind = props.other?.subagent_kind
  const tier = props.other?.service_tier
  if (!kind && !tier) return null

  const review = kind === 'review'
  const label = review ? t('Review subagent') : t('Subagent')
  return (
    <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
      {tier && <ServiceTierBadge tier={tier} />}
      {kind && (
        <Tooltip>
          <TooltipTrigger
            render={
              <span
                role='img'
                aria-label={label}
                tabIndex={0}
                className='inline-flex cursor-help items-center gap-1 text-purple-600 dark:text-purple-400'
              />
            }
          >
            <HugeiconsIcon icon={Robot01Icon} size={15} aria-hidden='true' />
            {review && (
              <HugeiconsIcon
                icon={FileValidationIcon}
                size={15}
                aria-hidden='true'
              />
            )}
          </TooltipTrigger>
          <TooltipContent>{`${label}: ${kind}`}</TooltipContent>
        </Tooltip>
      )}
    </div>
  )
}
