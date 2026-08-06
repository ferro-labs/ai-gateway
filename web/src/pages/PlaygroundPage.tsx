import { ImageIcon, MessagesSquare, Ruler } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '../auth/AuthProvider'
import { Button, EmptyState, LoadingState, Notice, PageHeader } from '../components/ui'
import { PlaygroundChat } from '../components/PlaygroundChat'
import { PlaygroundEmbeddings } from '../components/PlaygroundEmbeddings'
import { PlaygroundImages } from '../components/PlaygroundImages'
import { request } from '../lib/api'
import { useLoad } from '../lib/useLoad'
import type { CatalogModelsResponse } from '../lib/playground'
import type { Capabilities } from '../types'

type PlaygroundTab = 'chat' | 'embeddings' | 'images'

export default function PlaygroundPage() {
  const { isAdmin } = useAuth()
  const [tab, setTab] = useState<PlaygroundTab>('chat')

  // One model list for all three tabs: it is the same endpoint, each tab
  // filters it differently, and three copies would mean three refreshes and
  // three chances to disagree about what the gateway currently serves.
  const models = useLoad<CatalogModelsResponse>(
    (signal) => request<CatalogModelsResponse>('/v1/models', { signal }),
    [],
    'Models could not be loaded.',
  )
  const capabilities = useLoad<Capabilities>(
    (signal) => request<Capabilities>('/v1/capabilities', { signal }),
    [],
    'Provider capabilities could not be loaded.',
  )

  const catalog = models.data?.data ?? []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Playground"
        description="Exercise the chat, embeddings, and image routes through the gateway routing path."
      />
      {!isAdmin ? (
        <Notice>
          Read-only sessions can still send requests here — each one is billed by the upstream provider like any other completion.
        </Notice>
      ) : null}
      {models.error ? <Notice tone="error">{models.error}</Notice> : null}
      {/*
       * Advisory, not a failure: without the matrix the panels simply stop
       * warning about parameters a provider cannot express. Rendering that as a
       * red error would put a broken-looking banner above three surfaces that
       * all still work.
       */}
      {capabilities.error ? (
        <Notice tone="warning">
          Parameter support could not be read, so this page cannot say which settings a provider will discard.
        </Notice>
      ) : null}

      {models.loading && catalog.length === 0 ? <LoadingState label="Loading models" /> : null}

      {!models.loading && catalog.length === 0 && !models.error ? (
        <EmptyState
          action={<Button nativeButton={false} render={<Link to="/providers" />} variant="outline">Go to Providers</Button>}
          description="Connect a provider to make its models available here."
          title="No models available"
        />
      ) : null}

      {catalog.length > 0 || models.loading ? (
        <Tabs value={tab} onValueChange={(value) => setTab(value as PlaygroundTab)}>
          <TabsList aria-label="Gateway routes">
            <TabsTrigger value="chat">
              <MessagesSquare aria-hidden="true" size={16} />
              Chat
            </TabsTrigger>
            <TabsTrigger value="embeddings">
              <Ruler aria-hidden="true" size={16} />
              Embeddings
            </TabsTrigger>
            <TabsTrigger value="images">
              <ImageIcon aria-hidden="true" size={16} />
              Images
            </TabsTrigger>
          </TabsList>

          {/*
           * Each panel is unmounted while its tab is inactive, which is what
           * makes its cleanup abort an in-flight request. A generation left
           * running behind a tab the operator has moved away from would keep
           * billing and then write into state nobody is looking at.
           */}
          <TabsContent className="mt-3" value="chat">
            <PlaygroundChat
              capabilities={capabilities.data}
              loadingModels={models.loading}
              models={catalog}
              onRefreshModels={models.refresh}
            />
          </TabsContent>

          <TabsContent className="mt-3" value="embeddings">
            <PlaygroundEmbeddings
              loadingModels={models.loading}
              models={catalog}
              onRefreshModels={models.refresh}
            />
          </TabsContent>

          <TabsContent className="mt-3" value="images">
            <PlaygroundImages
              loadingModels={models.loading}
              models={catalog}
              onRefreshModels={models.refresh}
            />
          </TabsContent>
        </Tabs>
      ) : null}
    </div>
  )
}
